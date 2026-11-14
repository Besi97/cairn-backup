package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func compactArgs() []string {
	return []string{"compact", "--repository", "repo"}
}

func TestCompactInvalidInvocation(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	argsList := [][]string{{"compact"}}
	for _, extra := range []string{"--repository=", "--repository=repo::archive",
		"--archive-prefix=documents", "--archive-name=name", "--keep-daily=1", "--keep-within=2d",
		"--source-root=.", "--exclude=*.tmp", "--dry-run", "--dry-run=false", "--threshold=0",
		"--cleanup-commits", "--force", "--yes", "--passphrase=secret", "--passphrase-file=",
		"--identity-file=", "--identity-file=identity", "--lock-wait=-1", "--lock-wait=0x10",
		"--lock-wait=1.5", "--lock-wait=", "--lock-wait=9999999999999999999999999", "source"} {
		argsList = append(argsList, append(compactArgs(), extra))
	}
	argsList = append(argsList, append(compactArgs(), "--", "--dry-run"))
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

func TestCompactInvocation(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	// Explicit commands must not discover unrelated configuration.
	if err := os.WriteFile("cairn.yaml", []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BORG_REPO", "/must-not-be-used")
	record, calls := filepath.Join(t.TempDir(), "record"), filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_CALLS", calls)
	for _, wait := range []string{"60", "0", "08"} {
		var stdout, stderr bytes.Buffer
		args := []string{"compact", "--repository=repo with spaces"}
		if wait != "60" {
			args = append(args, "--lock-wait="+wait)
		}
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, &stderr)
		}
		seconds, _ := strconv.Atoi(wait)
		want := []string{"compact", "--lock-wait", strconv.Itoa(seconds), "--verbose", "--progress", "--", filepath.Join(cwd, "repo with spaces")}
		if got := readRecord(t, record); !reflect.DeepEqual(got.Args, want) || got.Dir != cwd {
			t.Fatalf("invocation = %+v, want %q", got, want)
		}
		if stdout.String() != "Compact completed.\n" {
			t.Fatalf("stdout = %q", &stdout)
		}
		for _, text := range []string{"Compact scope: whole repository", "stdout diagnostic", "stderr diagnostic"} {
			if !strings.Contains(stderr.String(), text) {
				t.Errorf("missing %q in stderr: %s", text, &stderr)
			}
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil || bytes.Count(data, []byte("\n")) != 3 {
		t.Fatalf("expected only three compactions, got %s: %v", data, err)
	}
}

func TestCompactAccess(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	record := filepath.Join(t.TempDir(), "record")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_READ_PASSPHRASE", "yes")
	t.Setenv("BORG_RSH", "must-not-run")
	t.Setenv("BORG_PASSPHRASE", "ignored")
	t.Setenv("BORG_PASSCOMMAND", "must-not-run")
	t.Setenv("BORG_PASSPHRASE_FD", "invalid")
	secret := []byte("optional compact credentials")
	if err := os.WriteFile("secret", secret, 0600); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"ssh://user@host:2222/./repo", "user@host:repo"} {
		var stdout, stderr bytes.Buffer
		args := []string{"compact", "--repository", location, "--identity-file=identity", "--passphrase-file=secret"}
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, &stderr)
		}
		got := readRecord(t, record).Args
		if got[0] != "--rsh" || got[1] != "ssh -oBatchMode=yes -oStrictHostKeyChecking=yes -i '"+filepath.Join(cwd, "identity")+"'" ||
			got[len(got)-1] != location {
			t.Fatalf("args = %q", got)
		}
		if strings.Contains(stdout.String()+stderr.String(), string(secret)) ||
			!strings.Contains(stderr.String(), fmt.Sprintf("passphrase digest: %x", sha256.Sum256(secret))) {
			t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), append(compactArgs(), "--passphrase-file=missing"), &stdout, &stderr, "test"); code != 2 ||
		!strings.Contains(stderr.String(), "open passphrase file") {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
}

func TestCompactOutcomes(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Setenv("BORG_EXIT_CODES", "modern")
	for _, tt := range []struct {
		raw, code int
		out       string
	}{
		{0, 0, "completed"}, {1, 1, "completed with warnings"}, {100, 1, "completed with warnings"},
		{2, 2, "failed"}, {3, 2, "failed"}, {99, 2, "failed"}, {130, 130, "interrupted"}, {143, 143, "interrupted"},
	} {
		t.Run(strconv.Itoa(tt.raw), func(t *testing.T) {
			t.Setenv("FAKE_EXIT", strconv.Itoa(tt.raw))
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), compactArgs(), &stdout, &stderr, "test"); code != tt.code {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Compact "+tt.out+".\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestCompactPreflightFailures(t *testing.T) {
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
			if code := Run(t.Context(), compactArgs(), &stdout, &stderr, "test"); code != 2 {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Compact failed.\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatal("operation ran after preflight failure")
			}
		})
	}
}

func TestCompactOutputFailures(t *testing.T) {
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
			if code := Run(t.Context(), compactArgs(), out, diagnostics, "test"); code != 2 {
				t.Fatalf("code = %d", code)
			}
		})
	}
}
