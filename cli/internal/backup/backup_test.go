package backup

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

func TestArchiveGlobSelectsOnlyPrefixArchives(t *testing.T) {
	pattern := archiveGlob("service")
	matches := map[string]bool{
		"service-2026-11-11T10:00:00":   true,
		"service-b-2026-11-11T10:00:00": false,
		"service-2026-11-11T10:00:00-x": false,
		"service":                       false,
	}
	for name, want := range matches {
		got, err := path.Match(pattern, name)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("match(%q, %q) = %v, want %v", pattern, name, got, want)
		}
	}
}

// fakeBorg installs a borg script that answers --version and runs body for
// other invocations. Recorded values are written to FAKE_BORG_DIR.
func fakeBorg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 'borg 1.4.5'; exit 0 ;; esac\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "borg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BORG_DIR", dir)
	return dir
}

const recordCreate = `printf '%s\n' "$@" > "$FAKE_BORG_DIR/args"
printf '%s\n' "${BORG_PASSPHRASE-unset}" > "$FAKE_BORG_DIR/passphrase"
printf '%s\n' "${BORG_RSH-unset}" > "$FAKE_BORG_DIR/rsh"
pwd -P > "$FAKE_BORG_DIR/cwd"
exit "${FAKE_BORG_EXIT:-0}"`

// fixture writes a configuration with a local repository and a secret file,
// and returns the loaded configuration and the source root.
func fixture(t *testing.T, location, repositoryExtra string) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "data")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "passphrase"), []byte("fixture-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document := "version: 1\nrepositories:\n  local:\n    location: " + location + "\n    passphrase_file: ./passphrase\n" + repositoryExtra +
		"jobs:\n  service:\n    repository: local\n    source_root: ./data\n    sources: [service/data, service/config]\n    archive_prefix: service\n"
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadValidated(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, source
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(data), "\n")
}

func TestRunRecordsArgumentsAndEnvironment(t *testing.T) {
	dir := fakeBorg(t, recordCreate)
	cfg, source := fixture(t, "ssh://backup@example.invalid/srv/backups/cairn", "    identity_file: ./id_ed25519\n    lock_wait: 5m\n")

	status, err := Run(context.Background(), cfg, "service", io.Discard, io.Discard)
	if err != nil || status != Succeeded {
		t.Fatalf("status = %v, err = %v", status, err)
	}

	wantArgs := "create\n--lock-wait\n300\n--stats\n--\nssh://backup@example.invalid/srv/backups/cairn::service-{now:%Y-%m-%dT%H:%M:%S}\n./service/data\n./service/config"
	if got := read(t, dir, "args"); got != wantArgs {
		t.Fatalf("args:\n%s\nwant:\n%s", got, wantArgs)
	}
	if got := read(t, dir, "passphrase"); got != "fixture-passphrase" {
		t.Fatalf("passphrase was not passed through the environment: %q", got)
	}
	wantRSH := "ssh -i '" + filepath.Join(filepath.Dir(cfg.Path()), "id_ed25519") + "'"
	if got := read(t, dir, "rsh"); got != wantRSH {
		t.Fatalf("BORG_RSH = %q, want %q", got, wantRSH)
	}
	wantCWD, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "cwd"); got != wantCWD {
		t.Fatalf("cwd = %q, want %q", got, wantCWD)
	}
}

func TestRunOmitsSSHSettingsWithoutIdentityFile(t *testing.T) {
	dir := fakeBorg(t, recordCreate)
	cfg, _ := fixture(t, "./repository", "")
	t.Setenv("BORG_RSH", "")
	if _, err := Run(context.Background(), cfg, "service", io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := read(t, dir, "args"); !strings.Contains(got, "--lock-wait\n60\n") {
		t.Fatalf("default lock_wait not applied: %s", got)
	}
}

func TestRunExitStatusMapping(t *testing.T) {
	cfg, _ := fixture(t, "./repository", "")
	for _, test := range []struct {
		exit   string
		status Status
	}{
		{"0", Succeeded},
		{"1", Warned},
		{"2", Failed},
		{"3", Failed},
		{"99", Failed},
		{"100", Warned},
		{"107", Warned},
		{"127", Warned},
		{"128", Failed},
		{"130", Failed},
	} {
		t.Run("exit "+test.exit, func(t *testing.T) {
			fakeBorg(t, recordCreate)
			t.Setenv("FAKE_BORG_EXIT", test.exit)
			status, err := Run(context.Background(), cfg, "service", io.Discard, io.Discard)
			if status != test.status {
				t.Fatalf("status = %v, want %v", status, test.status)
			}
			if (test.status == Failed) != (err != nil) {
				t.Fatalf("error = %v for status %v", err, status)
			}
		})
	}
}

func TestRunMissingBorgFails(t *testing.T) {
	cfg, _ := fixture(t, "./repository", "")
	t.Setenv("PATH", t.TempDir())
	status, err := Run(context.Background(), cfg, "service", io.Discard, io.Discard)
	if status != Failed || err == nil {
		t.Fatalf("status = %v, err = %v", status, err)
	}
}

func TestRunInterruptStopsBorgAndWaits(t *testing.T) {
	dir := fakeBorg(t, `trap 'kill $! 2>/dev/null; printf term > "$FAKE_BORG_DIR/term"; exit 143' TERM
: > "$FAKE_BORG_DIR/ready"
sleep 30 >/dev/null 2>&1 & wait $!`)
	cfg, _ := fixture(t, "./repository", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		status Status
		err    error
	}
	results := make(chan result, 1)
	go func() {
		status, err := Run(ctx, cfg, "service", io.Discard, io.Discard)
		results <- result{status, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake borg did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case got := <-results:
		if got.status != Interrupted || got.err != nil {
			t.Fatalf("status = %v, err = %v", got.status, got.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if got := read(t, dir, "term"); got != "term" {
		t.Fatalf("borg did not receive SIGTERM: %q", got)
	}
}

func TestLockWaitSeconds(t *testing.T) {
	for value, want := range map[string]int{"1s": 1, "60s": 60, "600s": 600, "1m": 60, "10m": 600} {
		got, err := lockWaitSeconds(value)
		if err != nil || got != want {
			t.Fatalf("%s = %d, %v; want %d", value, got, err, want)
		}
	}
	if _, err := lockWaitSeconds(""); err == nil {
		t.Fatal("accepted empty lock_wait")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/keys/it's a key"); got != `'/keys/it'\''s a key'` {
		t.Fatalf("shellQuote = %s", got)
	}
}
