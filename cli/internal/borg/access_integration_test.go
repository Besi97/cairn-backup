package borg

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt in with CAIRN_TEST_BORG=/absolute/path/to/borg. Each run uses fresh local
// repositories and isolated Borg state; no user repository or SSH host is used.
func TestRealBorgAccess(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	runner := Runner{Executable: binary}
	for _, mode := range []string{"repokey", "none", "empty-passphrase"} {
		t.Run(mode, func(t *testing.T) {
			repository := filepath.Join(t.TempDir(), "repo")
			passphrase, encryption := "integration secret", "repokey"
			if mode == "none" {
				encryption = "none"
			}
			if mode != "repokey" {
				passphrase = ""
			}
			init := exec.CommandContext(ctx, binary, "init", "--encryption="+encryption, repository)
			init.Env = environment(map[string]string{"BORG_PASSPHRASE": passphrase})
			if out, err := init.CombinedOutput(); err != nil {
				t.Fatalf("initialize disposable repository: %v\n%s", err, out)
			}
			path := filepath.Join(t.TempDir(), "passphrase")
			if err := os.WriteFile(path, []byte(passphrase+"\n\n"), 0600); err != nil {
				t.Fatal(err)
			}
			run := func(req Request, want Outcome) {
				t.Helper()
				var output bytes.Buffer
				req.Diagnostics = &output
				result, err := runner.Run(ctx, req)
				if result.Outcome != want || (err != nil) != (want == Failure) {
					t.Fatalf("result = %+v, error = %v\n%s", result, err, &output)
				}
				if passphrase != "" && strings.Contains(output.String(), passphrase) {
					t.Error("secret leaked into diagnostics")
				}
				if strings.Contains(output.String(), "Enter passphrase") {
					t.Error("Borg attempted an interactive passphrase query")
				}
			}
			run(Request{
				Args:   []string{"list", repository},
				Access: AccessOptions{PassphraseFile: path},
				Env: map[string]string{
					"BORG_PASSPHRASE":    "ignored",
					"BORG_PASSCOMMAND":   "must-not-run",
					"BORG_PASSPHRASE_FD": "invalid",
				},
			}, Success)
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			run(Request{Args: []string{"list", repository}, Env: map[string]string{"BORG_PASSPHRASE_FD": strconv.FormatUint(uint64(file.Fd()), 10)}}, Success)
			run(Request{Args: []string{"list", repository}, Env: map[string]string{"BORG_PASSPHRASE": passphrase, "BORG_PASSPHRASE_FD": "invalid"}}, Success)
			// A passcommand is executed by Borg, not by Cairn or a shell.
			run(Request{Args: []string{"list", repository}, Env: map[string]string{"BORG_PASSCOMMAND": "cat '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'", "BORG_PASSPHRASE_FD": "invalid"}}, Success)
			want := Success
			if mode == "repokey" {
				want = Failure
			}
			run(Request{Args: []string{"list", repository}}, want)
			if mode == "repokey" {
				run(Request{Args: []string{"list", repository}, Env: map[string]string{"BORG_PASSPHRASE": "wrong"}}, Failure)
			}
			run(Request{Args: []string{"compact", repository}}, Success)
		})
	}
	t.Run("SSH transport", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(fakeBorg, filepath.Join(dir, "ssh")); err != nil {
			t.Fatal(err)
		}
		record := filepath.Join(dir, "ssh.json")
		identity := filepath.Join(dir, "identity 'quoted' $(false); *")
		_, err := runner.Run(ctx, Request{
			Args:   []string{"list", "ssh://user@example.invalid:2222/./repo"},
			Access: AccessOptions{SSH: true, IdentityFile: identity},
			Env:    map[string]string{"PATH": dir + ":" + os.Getenv("PATH"), "FAKE_SSH_RECORD": record, "BORG_RSH": "must-not-run"},
		})
		if err == nil {
			t.Fatal("fake SSH unexpectedly succeeded")
		}
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		var args []string
		if err := json.Unmarshal(data, &args); err != nil {
			t.Fatal(err)
		}
		want := []string{"-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-i", identity, "-p", "2222", "user@example.invalid"}
		if len(args) < len(want) || !reflect.DeepEqual(args[:len(want)], want) {
			t.Fatalf("unexpected SSH arguments: %q", args)
		}
	})
}
