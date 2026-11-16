package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealBorgWorkflow(t *testing.T) {
	for _, mode := range []string{"unencrypted", "inline", "empty inline", "file"} {
		t.Run(mode, func(t *testing.T) {
			binary := realBorgEnvironment(t)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			base := t.TempDir()
			repository := filepath.Join(base, "repo")
			secret, encryption, credential := "", "none", ""
			if mode != "unencrypted" {
				encryption = "repokey"
				if mode != "empty inline" {
					secret = "workflow literal $HOME {hostname}"
				}
				credential = fmt.Sprintf(", passphrase: %q", secret)
			}
			if mode == "file" {
				credential = ", passphrase_file: ./passphrase"
				if err := os.WriteFile(filepath.Join(base, "passphrase"), []byte(secret+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			borgOutput(t, ctx, binary, secret, "init", "--encryption="+encryption, repository)
			seedArchives(t, ctx, binary, repository, secret, []archiveFixture{
				{"docs-old", "2020-01-01T12:00:00"}, {"other-keep", "2020-01-01T12:00:00"},
			})
			if err := os.Mkdir(filepath.Join(base, "source"), 0700); err != nil {
				t.Fatal(err)
			}
			const contents = "configured workflow contents\n"
			for _, name := range []string{"payload", "skip.tmp"} {
				if err := os.WriteFile(filepath.Join(base, "source", name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			text := fmt.Sprintf(`version: 1
repositories:
  local: {location: ./repo%s}
workflows:
  manual:
    stages:
      - tasks:
          - {type: backup, repository: local, archive_name: docs-current, source_root: ./source, sources: [payload, skip.tmp], exclude: ["*.tmp"]}
      - tasks:
          - {type: prune, repository: local, archive_prefix: docs, keep_daily: 1}
      - tasks:
          - {type: compact, repository: local}
      - tasks:
          - {type: check, repository: local}
      - tasks:
          - {type: check, repository: local, verify_data: true}
  compact-only:
    stages:
      - tasks: [{type: compact, repository: local}]
`, credential)
			path := filepath.Join(base, "config.yaml")
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "unencrypted" {
				t.Setenv("BORG_PASSPHRASE", "inherited wrong credential")
				t.Setenv("BORG_PASSCOMMAND", "must-not-run")
				t.Setenv("BORG_PASSPHRASE_FD", "invalid")
			}
			var stdout, stderr bytes.Buffer
			code := Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &stdout, &stderr, "test")
			if code != 0 {
				t.Fatalf("code=%d out=%s err=%s", code, &stdout, &stderr)
			}
			if secret != "" && strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatal("configured credential leaked")
			}
			for _, text := range []string{"manual 4.1 check/local", "manual 5.1 check/local", "Starting repository check", "Starting archive consistency check"} {
				if !strings.Contains(stderr.String(), text) {
					t.Errorf("missing check diagnostics %q: %s", text, &stderr)
				}
			}
			assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, secret), "docs-current", "other-keep")
			got := borgOutput(t, ctx, binary, secret, "extract", "--stdout", repository+"::docs-current", "payload")
			if string(got) != contents {
				t.Fatalf("archive contents = %q", got)
			}
			listing := borgOutput(t, ctx, binary, secret, "list", "--short", repository+"::docs-current")
			if strings.Contains(string(listing), "skip.tmp") {
				t.Fatal("configured exclusion ignored")
			}
			if mode == "file" {
				if err := os.Remove(filepath.Join(base, "passphrase")); err != nil {
					t.Fatal(err)
				}
				// Compaction needs neither the configured file nor valid inherited
				// encryption credentials. Keep only an incorrect inherited secret.
				if err := os.Unsetenv("BORG_PASSPHRASE_FD"); err != nil {
					t.Fatal(err)
				}
				stdout.Reset()
				stderr.Reset()
				if code := Run(ctx, []string{"workflow", "run", "compact-only", "--config", path}, &stdout, &stderr, "test"); code != 0 {
					t.Fatalf("compact without key: code=%d err=%s", code, &stderr)
				}
			}
		})
	}
}

func TestRealBorgWorkflowConcurrentCredentials(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	base := t.TempDir()
	const inline, fileSecret = "first literal $HOME {hostname}", "different file secret"
	for name, secret := range map[string]string{"first": inline, "second": fileSecret} {
		borgOutput(t, ctx, binary, secret, "init", "--encryption=repokey", filepath.Join(base, name))
	}
	const contents = "parallel configured backup\n"
	for name, data := range map[string]string{"payload": contents, "passphrase": fileSecret + "\n"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(base, "config.yaml")
	text := fmt.Sprintf(`version: 1
repositories:
  first: {location: ./first, passphrase: %q}
  second: {location: ./second, passphrase_file: ./passphrase}
workflows:
  parallel:
    stages:
      - tasks:
          - {type: backup, repository: first, archive_name: snapshot, sources: [payload]}
          - {type: backup, repository: second, archive_name: snapshot, sources: [payload]}
`, inline)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BORG_PASSPHRASE", "inherited wrong secret")
	t.Setenv("BORG_PASSCOMMAND", "must-not-run")
	t.Setenv("BORG_PASSPHRASE_FD", "invalid")
	var output bytes.Buffer
	// Both streams share a writer to exercise synchronization with real output.
	if code := Run(ctx, []string{"workflow", "run", "parallel", "--config", path}, &output, &output, "test"); code != 0 {
		t.Fatalf("code=%d output=%s", code, &output)
	}
	if strings.Contains(output.String(), inline) || strings.Contains(output.String(), fileSecret) {
		t.Fatal("concurrent credential leaked")
	}
	for name, secret := range map[string]string{"first": inline, "second": fileSecret} {
		repository := filepath.Join(base, name)
		assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, secret), "snapshot")
		if got := borgOutput(t, ctx, binary, secret, "extract", "--stdout", repository+"::snapshot", "payload"); string(got) != contents {
			t.Fatalf("%s archive contents = %q", name, got)
		}
	}
}
