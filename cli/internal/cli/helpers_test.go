package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

var fakeBorg string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cairn-cli-tests-")
	if err != nil {
		panic(err)
	}
	fakeBorg = filepath.Join(dir, "borg")
	// Share the runner's executable fixture, not test-specific helper functions.
	build := exec.Command("go", "build", "-o", fakeBorg, "../borg/testdata/fakeborg")
	if out, err := build.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		panic(string(out) + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func fakeBorgEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", filepath.Dir(fakeBorg))
	t.Setenv("FAKE_VERSION", "borg 1.4.5")
	for _, key := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND", "BORG_PASSPHRASE_FD", "FAKE_EXIT", "FAKE_RELEASE", "FAKE_TREE", "FAKE_VERSION_EXIT", "FAKE_READ_PASSPHRASE", "FAKE_SIGNAL"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func readRecord(t *testing.T, path string) (record struct {
	Args []string
	Dir  string
}) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}

func realBorgEnvironment(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to exercise real Borg backups")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "borg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	for _, key := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND", "BORG_PASSPHRASE_FD"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	for _, key := range []string{"BORG_BASE_DIR", "BORG_CONFIG_DIR", "BORG_CACHE_DIR", "BORG_SECURITY_DIR", "BORG_KEYS_DIR"} {
		t.Setenv(key, filepath.Join(state, key))
	}
	t.Setenv("BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK", "yes")
	t.Setenv("BORG_RELOCATED_REPO_ACCESS_IS_OK", "no")
	t.Setenv("BORG_HOST_ID", "cairn-cli-integration")
	t.Setenv("BORG_EXIT_CODES", "modern")
	return binary
}

func borgOutput(t *testing.T, ctx context.Context, binary, passphrase string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = append(os.Environ(), "BORG_PASSPHRASE="+passphrase)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Borg %s: %v\n%s\n%s", args[0], err, &stdout, &stderr)
	}
	return stdout.Bytes()
}

func holdRepository(t *testing.T, ctx context.Context, binary, repository string) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	dir := t.TempDir()
	record, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	done := make(chan error, 1)
	go func() {
		_, err := (borg.Runner{Executable: binary}).Run(ctx, borg.Request{
			Args: []string{"with-lock", "--lock-wait", "0", repository, fakeBorg},
			Env:  map[string]string{"FAKE_RECORD": record, "FAKE_RELEASE": release},
		})
		done <- err
	}()
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("external lock holder did not stop")
		}
	})
	awaitFile(t, record)
}

func awaitLockAttempt(t *testing.T, repository string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		matches, err := filepath.Glob(filepath.Join(repository, "lock.exclusive.*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Borg did not attempt lock acquisition")
		case <-tick.C:
		}
	}
}
