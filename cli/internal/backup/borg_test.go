package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// realFixture is a local repository created with the real Borg executable,
// configured for one job. See requireBorg for when its tests are skipped.
type realFixture struct {
	t          *testing.T
	root       string
	repository string
	cfg        *config.Config
}

// requireBorg skips the calling test when borg is not on PATH. With
// CAIRN_REQUIRE_BORG=1 it fails instead, so CI cannot pass while skipping these
// tests.
func requireBorg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("borg"); err == nil {
		return
	}
	if os.Getenv("CAIRN_REQUIRE_BORG") == "1" {
		t.Fatal("borg is not installed, and CAIRN_REQUIRE_BORG=1 requires it")
	}
	t.Skip("borg is not installed")
}

func newRealFixture(t *testing.T, sources []string, files map[string]string) *realFixture {
	t.Helper()
	return newRealFixtureWithJob(t, sources, files, "")
}

// newRealFixtureWithJob is newRealFixture with extra lines appended to the job.
func newRealFixtureWithJob(t *testing.T, sources []string, files map[string]string, jobExtra string) *realFixture {
	t.Helper()
	requireBorg(t)
	root := t.TempDir()
	t.Setenv("BORG_BASE_DIR", filepath.Join(root, "borg-base"))

	for name, contents := range files {
		path := filepath.Join(root, "inputs", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "passphrase"), []byte("round-trip-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, len(sources))
	for i, source := range sources {
		quoted[i] = "'" + source + "'"
	}
	document := "version: 1\nrepositories:\n  local:\n    location: ./repository\n    passphrase_file: ./passphrase\n" +
		"jobs:\n  service:\n    repository: local\n    source_root: ./inputs\n    sources: [" + strings.Join(quoted, ", ") +
		"]\n    archive_prefix: service\n" + jobExtra
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadValidated(configPath)
	if err != nil {
		t.Fatal(err)
	}

	fixture := &realFixture{t: t, root: root, repository: filepath.Join(root, "repository"), cfg: cfg}
	if out, err := fixture.borg(root, "init", "--encryption=repokey-blake2", fixture.repository); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return fixture
}

func (f *realFixture) borg(dir string, args ...string) ([]byte, error) {
	command := exec.Command("borg", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "BORG_PASSPHRASE=round-trip-passphrase")
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	return output.Bytes(), err
}

// archives returns the archive names in the repository.
func (f *realFixture) archives() []string {
	f.t.Helper()
	list, err := f.borg(f.root, "list", "--short", f.repository)
	if err != nil {
		f.t.Fatalf("list: %v\n%s", err, list)
	}
	return strings.Fields(string(list))
}

// restore extracts an archive with native Borg and returns the target directory.
func (f *realFixture) restore(archive string) string {
	f.t.Helper()
	target := filepath.Join(f.root, "restore-"+archive)
	if err := os.MkdirAll(target, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if out, err := f.borg(target, "extract", f.repository+"::"+archive); err != nil {
		f.t.Fatalf("extract: %v\n%s", err, out)
	}
	return target
}

func assertRestored(t *testing.T, restored string, files map[string]string) {
	t.Helper()
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(restored, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestRealBorgRoundTrip backs up to a local repository with the real Borg
// executable and extracts it with native Borg.
func TestRealBorgRoundTrip(t *testing.T) {
	files := map[string]string{
		"service/data/alpha.txt":     "alpha contents\n",
		"service/data/nested/b.bin":  "\x00\x01\x02 binary",
		"service/config/app.conf":    "key = value\n",
		"service/data/-leading-dash": "option-like name",
	}
	fixture := newRealFixture(t, []string{"service/data", "service/config"}, files)

	status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard)
	if err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}
	archives := fixture.archives()
	if len(archives) != 1 || !strings.HasPrefix(archives[0], "service-") {
		t.Fatalf("archives = %q", archives)
	}
	assertRestored(t, fixture.restore(archives[0]), files)
}

// TestRealBorgArchiveGlobIsolation checks that a job's archive glob does not
// select an archive from another job whose prefix extends it.
func TestRealBorgArchiveGlobIsolation(t *testing.T) {
	files := map[string]string{"service/data/alpha.txt": "alpha"}
	fixture := newRealFixture(t, []string{"service/data"}, files)

	if status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard); err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}
	other := fixture.repository + "::service-b-2026-11-11T10:00:00"
	if out, err := fixture.borg(filepath.Join(fixture.root, "inputs"), "create", other, "./service/data"); err != nil {
		t.Fatalf("create other job archive: %v\n%s", err, out)
	}
	if len(fixture.archives()) != 2 {
		t.Fatalf("archives = %q, want two", fixture.archives())
	}

	list, err := fixture.borg(fixture.root, "list", "--short", "--glob-archives", archiveGlob("service"), fixture.repository)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, list)
	}
	selected := strings.Fields(string(list))
	if len(selected) != 1 || !strings.HasPrefix(selected[0], "service-") || strings.HasPrefix(selected[0], "service-b-") {
		t.Fatalf("glob selected %q, want only the service archive", selected)
	}
}

// TestRealBorgPruneKeepsOnlyJobArchives seeds archives for this job and for
// service-b before a real backup with retention runs. With keep_hourly: 1, prune
// removes every seeded service archive, since only the newest hour is kept. The
// two service-b archives would be removed the same way if the glob matched them,
// so they must survive.
func TestRealBorgPruneKeepsOnlyJobArchives(t *testing.T) {
	files := map[string]string{"service/data/alpha.txt": "alpha"}
	fixture := newRealFixtureWithJob(t, []string{"service/data"}, files,
		"    maintenance:\n      retention:\n        keep_hourly: 1\n")
	inputs := filepath.Join(fixture.root, "inputs")
	for _, name := range []string{
		"service-2020-01-01T10:00:00",
		"service-2020-01-01T10:30:00",
		"service-b-2020-01-01T10:00:00",
		"service-b-2020-01-01T10:30:00",
	} {
		if out, err := fixture.borg(inputs, "create", fixture.repository+"::"+name, "./service/data"); err != nil {
			t.Fatalf("seed %s: %v\n%s", name, err, out)
		}
	}

	if status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard); err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}

	var mine, theirs []string
	for _, name := range fixture.archives() {
		switch {
		case strings.HasPrefix(name, "service-b-"):
			theirs = append(theirs, name)
		case strings.HasPrefix(name, "service-"):
			mine = append(mine, name)
		}
	}
	if len(theirs) != 2 {
		t.Fatalf("service-b archives = %q, want both kept", theirs)
	}
	if len(mine) != 1 || strings.HasPrefix(mine[0], "service-2020-") {
		t.Fatalf("service archives = %q, want only the new backup", mine)
	}
}

// TestRealBorgCompactAfterPrune runs backup, prune, and compact with real Borg.
// The repository must pass a repository check afterwards, the new archive must
// restore, and another job's archives must survive.
func TestRealBorgCompactAfterPrune(t *testing.T) {
	files := map[string]string{"service/data/alpha.txt": "alpha contents"}
	fixture := newRealFixtureWithJob(t, []string{"service/data"}, files,
		"    maintenance:\n      retention:\n        keep_hourly: 1\n      compact: true\n")
	inputs := filepath.Join(fixture.root, "inputs")
	for _, name := range []string{"service-b-2020-01-01T10:00:00", "service-b-2020-01-01T10:30:00"} {
		if out, err := fixture.borg(inputs, "create", fixture.repository+"::"+name, "./service/data"); err != nil {
			t.Fatalf("seed %s: %v\n%s", name, err, out)
		}
	}

	status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard)
	if err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}
	if out, err := fixture.borg(fixture.root, "check", "--repository-only", fixture.repository); err != nil {
		t.Fatalf("check after compact: %v\n%s", err, out)
	}

	var mine string
	var theirs int
	for _, name := range fixture.archives() {
		switch {
		case strings.HasPrefix(name, "service-b-"):
			theirs++
		case strings.HasPrefix(name, "service-"):
			mine = name
		}
	}
	if theirs != 2 || mine == "" {
		t.Fatalf("archives = %q, want two service-b and the new service archive", fixture.archives())
	}
	assertRestored(t, fixture.restore(mine), files)
}

// TestRealBorgDashSource guards against Borg reading a bare "-" source as stdin.
func TestRealBorgDashSource(t *testing.T) {
	files := map[string]string{"-": "dash source contents"}
	fixture := newRealFixture(t, []string{"-"}, files)

	status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard)
	if err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}
	archives := fixture.archives()
	if len(archives) != 1 {
		t.Fatalf("archives = %q", archives)
	}
	restored := fixture.restore(archives[0])
	assertRestored(t, restored, files)
	if _, err := os.Stat(filepath.Join(restored, "stdin")); err == nil {
		t.Fatal("archive contains stdin instead of the configured source")
	}
}

// TestRealBorgWarningExitCodes checks that a missing source, which Borg reports
// as a warning while still creating the archive, maps to Warned in both legacy
// and modern exit-code modes.
func TestRealBorgWarningExitCodes(t *testing.T) {
	for _, mode := range []string{"legacy", "modern"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("BORG_EXIT_CODES", mode)
			files := map[string]string{"service/data.txt": "kept contents"}
			fixture := newRealFixture(t, []string{"service", "missing"}, files)

			status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard)
			if status != Warned || err != nil {
				t.Fatalf("status = %v, err = %v; want Warned", status, err)
			}
			archives := fixture.archives()
			if len(archives) != 1 {
				t.Fatalf("archives = %q", archives)
			}
			assertRestored(t, fixture.restore(archives[0]), files)
		})
	}
}

// createArchive creates an archive named name from source, a path relative to
// the inputs directory.
func (f *realFixture) createArchive(name, source string) {
	f.t.Helper()
	if out, err := f.borg(filepath.Join(f.root, "inputs"), "create", f.repository+"::"+name, source); err != nil {
		f.t.Fatalf("create %s: %v\n%s", name, err, out)
	}
}

// seedPrunable adds two service archives that keep_hourly: 1 would prune, and an
// archive of random data that is then deleted without compacting. A real
// compaction therefore has space to reclaim and changes the repository.
func (f *realFixture) seedPrunable() {
	f.t.Helper()
	f.createArchive("service-2020-01-01T10:00:00", "./service/data")
	f.createArchive("service-2020-01-01T10:30:00", "./service/data")

	unique := filepath.Join(f.root, "inputs", "unique")
	if err := os.MkdirAll(unique, 0o700); err != nil {
		f.t.Fatal(err)
	}
	data := make([]byte, 1<<20)
	if _, err := rand.Read(data); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unique, "blob"), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.createArchive("other-2020-01-01T09:00:00", "./unique")
	if err := os.RemoveAll(unique); err != nil {
		f.t.Fatal(err)
	}
	if out, err := f.borg(f.root, "delete", f.repository+"::other-2020-01-01T09:00:00"); err != nil {
		f.t.Fatalf("delete: %v\n%s", err, out)
	}
}

// repositorySnapshot records each regular file's size, modification time, and
// SHA-256, and each directory by name. Directory modification times are left out
// because Borg's lock directory changes them without changing repository
// content. Top-level lock.* entries are Borg's lock artifacts and are left out
// too.
func repositorySnapshot(t *testing.T, repository string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(rel, "lock.") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			snapshot[rel+"/"] = "dir"
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[rel] = fmt.Sprintf("size=%d mtime=%d sha256=%x", info.Size(), info.ModTime().UnixNano(), sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// snapshotChanges lists the entries that were added, removed, or changed between
// two snapshots.
func snapshotChanges(before, after map[string]string) []string {
	var changes []string
	for name, value := range before {
		if got, ok := after[name]; !ok {
			changes = append(changes, "removed "+name)
		} else if got != value {
			changes = append(changes, "changed "+name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changes = append(changes, "added "+name)
		}
	}
	slices.Sort(changes)
	return changes
}

// TestRealBorgPreviewLeavesRepositoryUnchanged checks that a preview changes no
// repository file, directory, or archive, with retention and compaction
// configured. Each case then runs a real backup on the same repository, which
// must change the snapshot. A snapshot that cannot see changes cannot pass.
func TestRealBorgPreviewLeavesRepositoryUnchanged(t *testing.T) {
	const retention = "    maintenance:\n      retention:\n        keep_hourly: 1\n"
	const compactNote = "Compaction is not previewed and was not run."
	for _, test := range []struct {
		name            string
		sources         []string
		jobExtra        string
		wantStatus      Status
		wantPrune       bool
		wantCompactNote bool
	}{
		{"retention only", []string{"service/data"}, retention, Succeeded, true, false},
		{"compaction only", []string{"service/data"}, "    maintenance:\n      compact: true\n", Succeeded, false, true},
		{"retention and compaction", []string{"service/data"}, retention + "      compact: true\n", Succeeded, true, true},
		{"retention and compaction after a backup warning", []string{"service/data", "missing"}, retention + "      compact: true\n      after_backup_warning: run\n", Warned, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealFixtureWithJob(t, test.sources, map[string]string{"service/data/alpha.txt": "alpha contents"}, test.jobExtra)
			fixture.seedPrunable()
			before := repositorySnapshot(t, fixture.repository)
			archives := fixture.archives()

			var stdout, stderr bytes.Buffer
			status, err := Preview(context.Background(), fixture.cfg, "service", &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if status != test.wantStatus || err != nil {
				t.Fatalf("status = %v, err = %v\n%s", status, err, output)
			}
			if !strings.Contains(output, "- service/data/alpha.txt") {
				t.Fatalf("preview did not list the source:\n%s", output)
			}
			if got := strings.Contains(output, "Would prune"); got != test.wantPrune {
				t.Fatalf("prune previewed = %v, want %v:\n%s", got, test.wantPrune, output)
			}
			if got := strings.Contains(stdout.String(), compactNote); got != test.wantCompactNote {
				t.Fatalf("compaction note = %v, want %v:\n%s", got, test.wantCompactNote, output)
			}

			if changes := snapshotChanges(before, repositorySnapshot(t, fixture.repository)); len(changes) != 0 {
				t.Fatalf("preview changed the repository: %v", changes)
			}
			if got := fixture.archives(); !slices.Equal(got, archives) {
				t.Fatalf("archives = %q, want %q", got, archives)
			}

			if status, err := Run(context.Background(), fixture.cfg, "service", io.Discard, io.Discard); status != test.wantStatus || err != nil {
				t.Fatalf("backup status = %v, err = %v", status, err)
			}
			if len(snapshotChanges(before, repositorySnapshot(t, fixture.repository))) == 0 {
				t.Fatal("a real backup did not change the snapshot; the snapshot cannot detect changes")
			}
		})
	}
}

// TestRealBorgPreviewReportsLockContention holds Borg's exclusive repository lock
// in another process. A prune preview must wait for lock_wait and then fail, while
// a create preview takes no lock and still succeeds.
func TestRealBorgPreviewReportsLockContention(t *testing.T) {
	fixture := newRealFixtureWithJob(t, []string{"service/data"}, map[string]string{"service/data/alpha.txt": "alpha"},
		"    maintenance:\n      retention:\n        keep_hourly: 1\n")
	fixture.cfg.Repositories["local"].LockWait = "1s"

	holder := exec.Command("borg", "with-lock", "--lock-wait", "60", fixture.repository, "sleep", "10")
	holder.Dir = fixture.root
	holder.Env = append(os.Environ(), "BORG_PASSPHRASE=round-trip-passphrase")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(fixture.repository, "lock.exclusive")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("borg with-lock did not take the repository lock")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var stdout, stderr bytes.Buffer
	status, err := Preview(context.Background(), fixture.cfg, "service", &stdout, &stderr)
	output := stdout.String() + stderr.String()
	if status != Failed || err == nil || !strings.Contains(output, "(timeout)") {
		t.Fatalf("prune preview status = %v, err = %v:\n%s", status, err, output)
	}

	fixture.cfg.Jobs["service"].Maintenance = nil
	stdout.Reset()
	stderr.Reset()
	status, err = Preview(context.Background(), fixture.cfg, "service", &stdout, &stderr)
	if status != Succeeded || err != nil {
		t.Fatalf("create preview status = %v, err = %v:\n%s", status, err, stdout.String()+stderr.String())
	}
}

// TestRealBorgPreviewSourceWarning checks that a missing source makes the create
// preview warn. Maintenance is then skipped unless after_backup_warning is run,
// as in a real backup.
func TestRealBorgPreviewSourceWarning(t *testing.T) {
	const retention = "    maintenance:\n      retention:\n        keep_hourly: 1\n"
	for _, test := range []struct {
		name      string
		jobExtra  string
		wantPrune bool
	}{
		{"skipped by default", retention, false},
		{"run when configured", retention + "      after_backup_warning: run\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealFixtureWithJob(t, []string{"service/data", "missing"}, map[string]string{"service/data/alpha.txt": "alpha"}, test.jobExtra)
			fixture.seedPrunable()

			var stdout, stderr bytes.Buffer
			status, err := Preview(context.Background(), fixture.cfg, "service", &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if status != Warned || err != nil {
				t.Fatalf("status = %v, err = %v:\n%s", status, err, output)
			}
			if !strings.Contains(output, "missing") {
				t.Fatalf("preview did not report the missing source:\n%s", output)
			}
			if got := strings.Contains(output, "Would prune"); got != test.wantPrune {
				t.Fatalf("prune previewed = %v, want %v:\n%s", got, test.wantPrune, output)
			}
			if got := strings.Contains(stdout.String(), "Maintenance was not previewed:"); got == test.wantPrune {
				t.Fatalf("maintenance skip note = %v with prune previewed = %v:\n%s", got, test.wantPrune, output)
			}
		})
	}
}
