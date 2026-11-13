package borg

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func isolateCredentials(t *testing.T) {
	t.Helper()
	for _, key := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND", "BORG_PASSPHRASE_FD"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func realBorgEnvironment(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to exercise real Borg")
	}
	isolateCredentials(t)
	state := t.TempDir()
	for _, key := range []string{"BORG_BASE_DIR", "BORG_CONFIG_DIR", "BORG_CACHE_DIR", "BORG_SECURITY_DIR", "BORG_KEYS_DIR"} {
		t.Setenv(key, filepath.Join(state, key))
	}
	// Explicit trust applies only to fresh, disposable test repositories.
	t.Setenv("BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK", "yes")
	t.Setenv("BORG_RELOCATED_REPO_ACCESS_IS_OK", "no")
	// Containers without a stable MAC can give uuid.getnode() a random value
	// per invocation. Borg must recognize these isolated runs as the same host
	// to exercise native stale-lock recovery rather than foreign-host locks.
	t.Setenv("BORG_HOST_ID", "cairn-integration")
	return binary
}

func disposableRepository(t *testing.T, ctx context.Context, binary string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command(binary, "init", "--encryption=none", repository)
	cmd.Env = environment(map[string]string{"BORG_PASSPHRASE": ""})
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := runProcess(ctx, cmd, shutdownGrace); err != nil {
		t.Fatalf("initialize disposable repository: %v\n%s", err, &output)
	}
	return repository
}

func awaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return data
		}
		select {
		case <-deadline.C:
			t.Fatalf("process did not create %s", path)
		case <-tick.C:
		}
	}
}
