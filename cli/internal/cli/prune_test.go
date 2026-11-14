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

func pruneArgs() []string {
	return []string{"prune", "--repository", "repo", "--archive-prefix", "documents", "--keep-daily", "7"}
}

func TestPruneInvalidInvocation(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	argsList := [][]string{
		{"prune"}, {"prune", "--archive-prefix", "documents", "--keep-daily", "7"},
		{"prune", "--repository", "repo", "--keep-daily", "7"},
		{"prune", "--repository", "repo", "--archive-prefix", "documents"},
		{"prune", "--repository", "repo", "--archive-prefix", "documents", "--keep-daily", "0", "--keep-weekly", "0"},
	}
	for _, extra := range []string{"--repository=", "--archive-prefix=", "--keep-within=", "--keep-within=0d",
		"--keep-within=-1d", "--keep-within=1h", "--keep-within=1.5d", "--keep-daily=", "--keep-daily=1.5",
		"--keep-daily=0x10", "--keep-weekly=9999999999999999999999999",
		"--keep-last=1", "--keep-13weekly=1", "--keep-3monthly=1", "--archive-name=name", "--source-root=.",
		"--exclude=*.tmp", "--force", "--yes", "--passphrase=secret", "--passphrase-file=", "--identity-file=",
		"--identity-file=identity", "--lock-wait=-1", "--lock-wait=0x10", "--lock-wait=1.5", "--lock-wait=",
		"--lock-wait=9999999999999999999999999", "source"} {
		argsList = append(argsList, append(pruneArgs(), extra))
	}
	argsList = append(argsList, append(pruneArgs(), "--", "--keep-weekly=4"))
	for _, args := range argsList {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 3 {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.Len() != 0 || strings.Count(stderr.String(), "Error:") != 1 ||
				!strings.Contains(stderr.String(), "for usage") || strings.Contains(stderr.String(), "find Borg") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestPruneInvocation(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	record, calls := filepath.Join(t.TempDir(), "record"), filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_CALLS", calls)
	secret := []byte("do not print prune credentials")
	if err := os.WriteFile("secret", secret, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_READ_PASSPHRASE", "yes")
	t.Setenv("BORG_PASSPHRASE", "ignored")
	t.Setenv("BORG_PASSCOMMAND", "must-not-run")
	t.Setenv("BORG_PASSPHRASE_FD", "invalid")
	var stdout, stderr bytes.Buffer
	args := []string{"prune", "--repository", "repo with spaces", "--archive-prefix=-{hostname}-[ab]*",
		"--keep-within=2m", "--keep-secondly=-1", "--keep-minutely=02", "--keep-hourly=3", "--keep-daily=4",
		"--keep-weekly=5", "--keep-monthly=6", "--keep-yearly=7", "--passphrase-file=secret", "--lock-wait=08"}
	if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	got := readRecord(t, record)
	want := []string{"prune", "--lock-wait", "8", "--list", "--stats", "--glob-archives=-{hostname}-[ab]*-*",
		"--keep-within=2m", "--keep-secondly=-1", "--keep-minutely=2", "--keep-hourly=3", "--keep-daily=4",
		"--keep-weekly=5", "--keep-monthly=6", "--keep-yearly=7", "--", filepath.Join(cwd, "repo with spaces")}
	if !reflect.DeepEqual(got.Args, want) || got.Dir != cwd {
		t.Fatalf("invocation = %+v, want %q", got, want)
	}
	data, err := os.ReadFile(calls)
	if err != nil || bytes.Count(data, []byte("\n")) != 1 {
		t.Fatalf("expected only one prune, got %s: %v", data, err)
	}
	if stdout.String() != "Prune completed.\n" || strings.Contains(stderr.String(), string(secret)) {
		t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
	}
	for _, text := range []string{"Prune archive selector: -{hostname}-[ab]*-*", "stdout diagnostic",
		"stderr diagnostic", fmt.Sprintf("passphrase digest: %x", sha256.Sum256(secret))} {
		if !strings.Contains(stderr.String(), text) {
			t.Errorf("missing %q in stderr: %s", text, &stderr)
		}
	}
}

func TestPruneOutcomes(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Setenv("BORG_EXIT_CODES", "modern")
	for _, preview := range []bool{false, true} {
		for _, tt := range []struct {
			raw  int
			code int
			out  string
		}{
			{0, 0, "completed"}, {1, 1, "completed with warnings"}, {100, 1, "completed with warnings"},
			{2, 2, "failed"}, {3, 2, "failed"}, {99, 2, "failed"}, {130, 130, "interrupted"}, {143, 143, "interrupted"},
		} {
			t.Run(fmt.Sprintf("%t/%d", preview, tt.raw), func(t *testing.T) {
				t.Setenv("FAKE_EXIT", strconv.Itoa(tt.raw))
				args := pruneArgs()
				operation, suffix := "Prune", ".\n"
				if preview {
					args = append(args, "--dry-run")
					operation = "Prune preview"
					if tt.code < 2 {
						suffix = "; no archives deleted.\n"
					}
				}
				var stdout, stderr bytes.Buffer
				if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != tt.code {
					t.Fatalf("code = %d, stderr = %s", code, &stderr)
				}
				if stdout.String() != operation+" "+tt.out+suffix || strings.Contains(stderr.String(), "for usage") {
					t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
				}
			})
		}
	}
}

func TestPrunePreview(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), append(pruneArgs(), "--dry-run", "--lock-wait=0"), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	cwd, _ := os.Getwd()
	want := []string{"prune", "--lock-wait", "0", "--list", "--glob-archives=documents-*",
		"--keep-daily=7", "--dry-run", "--", filepath.Join(cwd, "repo")}
	if got := readRecord(t, record).Args; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestPruneSSH(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("BORG_RSH", "must-not-run")
	cwd, _ := os.Getwd()
	for _, location := range []string{"ssh://user@host:2222/./repo", "user@host:repo"} {
		var stdout, stderr bytes.Buffer
		args := []string{"prune", "--repository", location, "--archive-prefix=documents", "--keep-daily=1", "--identity-file=identity"}
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, &stderr)
		}
		got := readRecord(t, record).Args
		if got[0] != "--rsh" || got[1] != "ssh -oBatchMode=yes -oStrictHostKeyChecking=yes -i '"+filepath.Join(cwd, "identity")+"'" ||
			got[len(got)-1] != location {
			t.Fatalf("args = %q", got)
		}
	}
}

func TestPrunePreflightFailures(t *testing.T) {
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
			if code := Run(t.Context(), pruneArgs(), &stdout, &stderr, "test"); code != 2 {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Prune failed.\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatal("operation ran after preflight failure")
			}
		})
	}
}

func TestPruneCancellation(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			fakeBorgEnvironment(t)
			base := filepath.Join(t.TempDir(), "ready")
			t.Setenv("FAKE_TREE", base)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() { done <- Run(ctx, pruneArgs(), &stdout, &stderr, "test") }()
			awaitFile(t, base+".parent")
			awaitFile(t, base+".child")
			cancel(cancellation.Signal(signal))
			select {
			case code := <-done:
				if code != 128+int(signal) || stdout.String() != "Prune interrupted.\n" ||
					strings.Count(stderr.String(), "Interrupted:") != 1 || strings.Contains(stderr.String(), "for usage") {
					t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("prune cancellation hung")
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

func TestPruneOutputFailures(t *testing.T) {
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
			if code := Run(t.Context(), pruneArgs(), out, diagnostics, "test"); code != 2 {
				t.Fatalf("code = %d", code)
			}
		})
	}
}
