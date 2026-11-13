package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBackupBinary(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	fakeBorg := buildExecutable(t, "borg", "../../internal/borg/testdata/fakeborg")
	for _, tt := range []struct {
		raw  string
		code int
	}{
		{"0", 0}, {"100", 1}, {"2", 2},
	} {
		t.Run("exit "+tt.raw, func(t *testing.T) {
			cmd := exec.Command(binary, "backup", "--repository", "repo", "--archive-name", "name", "source")
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=" + filepath.Dir(fakeBorg), "HOME=" + cmd.Dir, "FAKE_VERSION=borg 1.4.5", "FAKE_EXIT=" + tt.raw, "BORG_EXIT_CODES=modern"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			_ = cmd.Run()
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tt.code || !strings.Contains(stdout.String(), "Backup ") || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("exit = %v, stdout = %q, stderr = %q", cmd.ProcessState, &stdout, &stderr)
			}
		})
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(binary, "backup", "--repository", "repo", "--archive-name", "name", "source")
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"PATH=" + filepath.Dir(fakeBorg), "HOME=" + cmd.Dir, "FAKE_VERSION=borg 1.4.5", "FAKE_TREE=" + base}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-done
			})
			awaitFile(t, base+".parent")
			awaitFile(t, base+".child")
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				if cmd.ProcessState.ExitCode() != 128+int(sig) || stdout.String() != "Backup interrupted.\n" || strings.Count(stderr.String(), "Interrupted:") != 1 {
					t.Fatalf("exit = %v, stdout = %q, stderr = %q", cmd.ProcessState, &stdout, &stderr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("backup did not exit on signal")
			}
			for _, role := range []string{"parent", "child"} {
				data, err := os.ReadFile(base + "." + role + ".signal")
				if err != nil || string(data) != strconv.Itoa(int(sig)) {
					t.Fatalf("%s received %s: %v", role, data, err)
				}
			}
		})
	}
}
