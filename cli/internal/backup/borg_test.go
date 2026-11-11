package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// realFixture is a local repository created with the real Borg executable,
// configured for one job. It skips the test when borg is not on PATH.
type realFixture struct {
	t          *testing.T
	root       string
	repository string
	cfg        *config.Config
}

func newRealFixture(t *testing.T, sources []string, files map[string]string) *realFixture {
	t.Helper()
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg is not installed")
	}
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
		"]\n    archive_prefix: service\n"
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
