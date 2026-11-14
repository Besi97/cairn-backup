package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func backupArgs() []string {
	return []string{"backup", "--repository", "repo", "--archive-name", "archive", "source"}
}

func TestBackupInvalidInvocation(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	for _, args := range [][]string{
		{"backup"},
		{"backup", "--archive-name", "archive", "source"},
		{"backup", "--repository=", "--archive-name", "archive", "source"},
		{"backup", "--repository", "repo", "source"},
		{"backup", "--repository", "repo", "--archive-name=", "source"},
		{"backup", "--repository", "repo", "--archive-prefix=", "source"},
		{"backup", "--repository", "repo", "--archive-name=name", "--archive-prefix=prefix", "source"},
		{"backup", "--repository", "repo", "--archive-name=name", "--archive-prefix=", "source"},
		{"backup", "--repository", "repo", "--archive-name=name"},
		append(backupArgs(), "--lock-wait=-1"),
		append(backupArgs(), "--lock-wait=1.5"),
		append(backupArgs(), "--lock-wait=0x10"),
		append(backupArgs(), "--lock-wait=9999999999999999999999999"),
		append(backupArgs(), "--keep-daily=7"),
		append(backupArgs(), "--compression=zstd"),
		append(backupArgs(), "--passphrase=secret"),
		append(backupArgs(), "--source-root="),
		append(backupArgs(), "--passphrase-file="),
		append(backupArgs(), "--identity-file=id"),
		append(backupArgs(), "--", ""),
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != exitInvalidInput {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.Len() != 0 || strings.Count(stderr.String(), "Error:") != 1 || !strings.Contains(stderr.String(), "for usage") || strings.Contains(stderr.String(), "find Borg") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestBackupInvocation(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Chdir(t.TempDir())
	root := filepath.Join("sources", "root")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_CALLS", calls)
	secret := []byte("do not print this passphrase")
	if err := os.WriteFile("passphrase", secret, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_READ_PASSPHRASE", "yes")
	t.Setenv("BORG_PASSPHRASE", "ignored")
	t.Setenv("BORG_PASSCOMMAND", "must-not-run")
	t.Setenv("BORG_PASSPHRASE_FD", "invalid")
	var stdout, stderr bytes.Buffer
	args := []string{"backup", "--repository", "repo", "--archive-name", "{hostname}-Before Upgrade", "--source-root", root,
		"--exclude", "sh:a,b/*", "--exclude", "*.tmp", "--passphrase-file", "passphrase", "--lock-wait", "0", "--", "./folder/../folder", "/absolute", "-hyphen", "$(false)"}
	if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	cwd, _ := os.Getwd()
	got := readRecord(t, record)
	want := []string{"create", "--lock-wait", "0", "--stats", "--progress", "--exclude=sh:a,b/*", "--exclude=*.tmp", "--", filepath.Join(cwd, "repo") + "::{hostname}-Before Upgrade", "./folder/../folder", "/absolute", "-hyphen", "$(false)"}
	if !reflect.DeepEqual(got.Args, want) || got.Dir != filepath.Join(cwd, root) {
		t.Fatalf("invocation = %+v, want %q", got, want)
	}
	data, err := os.ReadFile(calls)
	if err != nil || bytes.Count(data, []byte("\n")) != 1 {
		t.Fatalf("expected exactly one Borg operation, got %s: %v", data, err)
	}
	if stdout.String() != "Backup completed.\n" || strings.Contains(stdout.String(), "diagnostic") || strings.Contains(stderr.String(), string(secret)) {
		t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
	}
	for _, text := range []string{"stdout diagnostic", "stderr diagnostic", fmt.Sprintf("passphrase digest: %x", sha256.Sum256(secret))} {
		if !strings.Contains(stderr.String(), text) {
			t.Errorf("missing %q in stderr: %s", text, &stderr)
		}
	}
}

func TestBackupOutcomes(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Setenv("BORG_EXIT_CODES", "modern")
	for _, tt := range []struct {
		raw  int
		code int
		out  string
	}{
		{0, 0, "completed"}, {1, 1, "completed with warnings"}, {100, 1, "completed with warnings"},
		{2, 2, "failed"}, {3, 2, "failed"}, {99, 2, "failed"}, {130, 130, "interrupted"}, {143, 143, "interrupted"},
	} {
		t.Run(strconv.Itoa(tt.raw), func(t *testing.T) {
			t.Setenv("FAKE_EXIT", strconv.Itoa(tt.raw))
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), backupArgs(), &stdout, &stderr, "test"); code != tt.code {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
			}
			if stdout.String() != "Backup "+tt.out+".\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
			wantErrors := 0
			if tt.code >= 2 {
				wantErrors = 1
			}
			if strings.Count(stderr.String(), "Error:") != wantErrors {
				t.Fatalf("unexpected diagnostic: %s", &stderr)
			}
		})
	}
}

func TestBackupPreflightFailures(t *testing.T) {
	for _, version := range []string{"", "borg 1.2.7", "borg 2.0.0", "borg 1.5.0rc1"} {
		t.Run(version, func(t *testing.T) {
			fakeBorgEnvironment(t)
			if version == "" {
				t.Setenv("PATH", "")
			} else {
				t.Setenv("FAKE_VERSION", version)
			}
			record := filepath.Join(t.TempDir(), "record")
			t.Setenv("FAKE_RECORD", record)
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), backupArgs(), &stdout, &stderr, "test"); code != 2 {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Backup failed.\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatal("operation ran after preflight failure")
			}
		})
	}
}

func TestBackupPreview(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), append(backupArgs(), "--dry-run"), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	args := readRecord(t, record).Args
	if !reflect.DeepEqual(args[:5], []string{"create", "--lock-wait", "60", "--dry-run", "--list"}) || stdout.String() != "Backup preview completed; no archive created.\n" {
		t.Fatalf("args = %q, stdout = %q", args, &stdout)
	}
}

func TestBackupDecimalLockWait(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), append(backupArgs(), "--lock-wait=08"), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	if got := readRecord(t, record).Args[:3]; !reflect.DeepEqual(got, []string{"create", "--lock-wait", "8"}) {
		t.Fatalf("args = %q", got)
	}
}

func TestBackupSSH(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("BORG_RSH", "must-not-run")
	cwd, _ := os.Getwd()
	var stdout, stderr bytes.Buffer
	args := []string{"backup", "--repository", "ssh://user@host:2222/./repo", "--archive-name", "archive", "--identity-file", "identity", "source"}
	if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	got := readRecord(t, record).Args
	if got[0] != "--rsh" || got[1] != "ssh -oBatchMode=yes -oStrictHostKeyChecking=yes -i '"+filepath.Join(cwd, "identity")+"'" || got[len(got)-2] != "ssh://user@host:2222/./repo::archive" {
		t.Fatalf("args = %q", got)
	}
}

func TestBackupCancellation(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			fakeBorgEnvironment(t)
			base := filepath.Join(t.TempDir(), "ready")
			t.Setenv("FAKE_TREE", base)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- Run(ctx, backupArgs(), &stdout, &stderr, "test") }()
			awaitFile(t, base+".parent")
			awaitFile(t, base+".child")
			cancel(cancellation.Signal(signal))
			select {
			case code := <-done:
				if code != 128+int(signal) || stdout.String() != "Backup interrupted.\n" || strings.Count(stderr.String(), "Interrupted:") != 1 || strings.Contains(stderr.String(), "for usage") {
					t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("backup cancellation hung")
			}
			for _, role := range []string{"parent", "child"} {
				data, err := os.ReadFile(base + "." + role + ".signal")
				if err != nil || string(data) != strconv.Itoa(int(signal)) {
					t.Fatalf("%s received %s: %v", role, data, err)
				}
			}
		})
	}
}

func TestBackupOutputFailures(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			fakeBorgEnvironment(t)
			var stdout, stderr bytes.Buffer
			out, diagnostics := io.Writer(&stdout), io.Writer(&stderr)
			if stream == "stdout" {
				out = brokenWriter{}
			} else {
				diagnostics = brokenWriter{}
			}
			if code := Run(t.Context(), backupArgs(), out, diagnostics, "test"); code != 2 {
				t.Fatalf("code = %d", code)
			}
			if strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("output failure treated as a usage error: %s", &stderr)
			}
		})
	}
}
