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

func checkArgs() []string {
	return []string{"check", "--repository", "repo"}
}

func TestCheckInvalidInvocation(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	argsList := [][]string{{"check"}}
	for _, extra := range []string{"--repository=", "--repository=repo::archive",
		"--archive-prefix=documents", "--archive-name=name", "--glob-archives=*", "--first=1", "--last=1",
		"--keep-daily=1", "--source-root=.", "--exclude=*.tmp", "--dry-run", "--repair", "--repair=false",
		"--repository-only", "--archives-only", "--max-duration=60", "--save-space", "--force", "--yes",
		"--verify-data=invalid", "--passphrase=secret", "--passphrase-file=", "--identity-file=",
		"--identity-file=identity", "--lock-wait=-1", "--lock-wait=0x10", "--lock-wait=1.5",
		"--lock-wait=", "--lock-wait=9999999999999999999999999", "source"} {
		argsList = append(argsList, append(checkArgs(), extra))
	}
	argsList = append(argsList, append(checkArgs(), "--", "--repair"))
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

func TestCheckHelpAndCompletion(t *testing.T) {
	t.Setenv("PATH", "")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"check", "--help"}, &stdout, &stderr, "test"); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	for _, text := range []string{"NOT cryptographically verified", "--verify-data", "never request repair", "--lock-wait", "warnings can indicate corruption or skipped checks"} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("help missing %q: %s", text, &stdout)
		}
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{""}, "check"},
		{[]string{"check", "--"}, "--verify-data"},
		{[]string{"check", "--repository", "./repo"}, ":0\n"},
		{[]string{"check", "--repository", "ssh://user@host/repo"}, ":4\n"},
		{[]string{"check", "--passphrase-file", "./secret"}, ":0\n"},
		{[]string{"check", "--identity-file", "./identity"}, ":0\n"},
		{[]string{"check", "--lock-wait", ""}, ":4\n"},
	} {
		code, out, errOut := runCompletion(t, tt.args...)
		if code != 0 || !strings.Contains(out, tt.want) {
			t.Fatalf("completion %q: code = %d, stdout = %q, stderr = %q", tt.args, code, out, errOut)
		}
	}
}

func TestCheckInvocation(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	if err := os.WriteFile("cairn.yaml", []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cwd)
	if err := os.Mkdir("cairn", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("cairn/config.yaml", []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BORG_REPO", "/must-not-be-used")
	record, calls := filepath.Join(t.TempDir(), "record"), filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_CALLS", calls)
	for _, tt := range []struct {
		flags  []string
		wait   string
		verify bool
	}{
		{nil, "60", false},
		{[]string{"--lock-wait=0", "--verify-data"}, "0", true},
		{[]string{"--lock-wait=08", "--verify-data=false"}, "8", false},
	} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"check", "--repository=repo with spaces"}, tt.flags...)
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("code = %d, stderr = %s", code, &stderr)
		}
		want := []string{"check", "--lock-wait", tt.wait, "--verbose", "--progress"}
		scope := "file contents are NOT cryptographically verified"
		if tt.verify {
			want = append(want, "--verify-data")
			scope = "cryptographic file-content verification"
		}
		want = append(want, "--", filepath.Join(cwd, "repo with spaces"))
		if got := readRecord(t, record); !reflect.DeepEqual(got.Args, want) || got.Dir != cwd {
			t.Fatalf("invocation = %+v, want %q", got, want)
		}
		if stdout.String() != "Check completed.\n" {
			t.Fatalf("stdout = %q", &stdout)
		}
		for _, text := range []string{"Check scope: whole repository consistency", "all archive metadata", scope, "no repair", "warnings can indicate corruption or skipped checks", "stdout diagnostic", "stderr diagnostic"} {
			if !strings.Contains(stderr.String(), text) {
				t.Errorf("missing %q in stderr: %s", text, &stderr)
			}
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil || bytes.Count(data, []byte("\n")) != 3 {
		t.Fatalf("expected only three checks, got %s: %v", data, err)
	}
}

func TestCheckAccess(t *testing.T) {
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
	secret := []byte("check credentials")
	if err := os.WriteFile("secret", secret, 0600); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"ssh://user@host:2222/./repo", "user@host:repo"} {
		var stdout, stderr bytes.Buffer
		args := []string{"check", "--repository", location, "--identity-file=identity", "--passphrase-file=secret"}
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
	if code := Run(t.Context(), append(checkArgs(), "--passphrase-file=missing"), &stdout, &stderr, "test"); code != 2 ||
		!strings.Contains(stderr.String(), "open passphrase file") {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
}

func TestCheckOutcomes(t *testing.T) {
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
			if code := Run(t.Context(), checkArgs(), &stdout, &stderr, "test"); code != tt.code {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Check "+tt.out+".\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestCheckPreflightFailures(t *testing.T) {
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
			if code := Run(t.Context(), checkArgs(), &stdout, &stderr, "test"); code != 2 {
				t.Fatalf("code = %d, stderr = %s", code, &stderr)
			}
			if stdout.String() != "Check failed.\n" || strings.Contains(stderr.String(), "for usage") {
				t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
			if _, err := os.Stat(record); !os.IsNotExist(err) {
				t.Fatal("operation ran after preflight failure")
			}
		})
	}
}

func TestCheckOutputFailures(t *testing.T) {
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
			if code := Run(t.Context(), checkArgs(), out, diagnostics, "test"); code != 2 {
				t.Fatalf("code = %d", code)
			}
		})
	}
}
