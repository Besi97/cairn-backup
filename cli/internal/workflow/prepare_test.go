package workflow

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/backup"
	"github.com/Besi97/cairn-backup/cli/internal/check"
	"github.com/Besi97/cairn-backup/cli/internal/compact"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/prune"
)

func TestPrepareOperationParity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	text := `version: 1
repositories:
  remote:
    location: "user@host:~/repo"
    passphrase_file: ./missing-secret
    identity_file: ./missing-key
workflows:
  manual:
    stages:
      - tasks:
          - type: backup
            repository: remote
            archive_prefix: "{hostname}"
            source_root: ./source
            sources: [".", "a/../b", "~/literal", "$HOME", "-name"]
            exclude: ["sh:**/*.tmp", "-literal"]
            lock_wait: 0
          - {type: backup, repository: remote, archive_name: "exact-{now}", sources: [.]}
          - {type: prune, repository: remote, archive_prefix: "{hostname}", keep_within: "+001d", keep_daily: -1, keep_weekly: 2}
          - {type: compact, repository: remote}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 10, 0, 0, 123, time.FixedZone("offset", 3600))
	plan, err := Prepare(selected, "manual", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(selected, "Manual", now); err == nil {
		t.Fatal("workflow lookup must be case sensitive")
	}
	zero := 0
	secret, identity := filepath.Join(dir, "missing-secret"), filepath.Join(dir, "missing-key")
	first, err := backup.Prepare(backup.Options{
		Repository: "user@host:~/repo", ArchivePrefix: "{hostname}", SourceRoot: filepath.Join(dir, "source"),
		Sources:  []string{".", "a/../b", "~/literal", "$HOME", "-name"},
		Excludes: []string{"sh:**/*.tmp", "-literal"}, LockWait: &zero,
		PassphraseFile: secret, IdentityFile: identity,
	}, dir, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backup.Prepare(backup.Options{
		Repository: "user@host:~/repo", ArchiveName: "exact-{now}", SourceRoot: dir,
		Sources: []string{"."}, PassphraseFile: secret, IdentityFile: identity,
	}, dir, now)
	if err != nil {
		t.Fatal(err)
	}
	third, err := prune.Prepare(prune.Options{
		Repository: "user@host:~/repo", ArchivePrefix: "{hostname}",
		Retention:      prune.Retention{Within: "+001d", Daily: -1, Weekly: 2},
		PassphraseFile: secret, IdentityFile: identity,
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := compact.Prepare(compact.Options{Repository: "user@host:~/repo", IdentityFile: identity}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []Task{
		{Type: "backup", Repository: "remote", Scope: "archive template: " + first.Archive, Request: first.Request},
		{Type: "backup", Repository: "remote", Scope: "archive template: " + second.Archive, Request: second.Request},
		{Type: "prune", Repository: "remote", Scope: "archive selector: " + third.Selector, Request: third.Request},
		{Type: "compact", Repository: "remote", Scope: "whole repository; reclaiming eligible space", Request: fourth},
	} {
		if !reflect.DeepEqual(plan.Stages[0][i], want) {
			t.Errorf("task %d differs from standalone preparation", i+1)
		}
	}
	if first.Archive != "{hostname}-20261114T090000.000000123Z" {
		t.Fatalf("archive = %q", first.Archive)
	}
}

func TestPrepareInlineCredentials(t *testing.T) {
	for _, secret := range []string{"", "literal $HOME {hostname}\nsecret"} {
		for _, operation := range []string{"backup", "prune", "compact", "check"} {
			t.Run(operation+"/"+secret, func(t *testing.T) {
				task := config.Task{
					Type: operation, Repository: "repo", ArchivePrefix: "docs",
					Sources: []string{"."}, KeepDaily: 1,
				}
				prepared, err := prepareTask(task, config.Repository{Location: "./repo", Passphrase: &secret}, t.TempDir(), time.Now())
				if err != nil {
					t.Fatal(err)
				}
				if operation == "compact" {
					if prepared.Request.Access.Passphrase != nil {
						t.Fatal("compact received encryption credential")
					}
				} else if prepared.Request.Access.Passphrase == nil || *prepared.Request.Access.Passphrase != secret {
					t.Fatal("inline credential presence/value lost")
				}
			})
		}
	}
}

func TestPrepareCheckParity(t *testing.T) {
	for _, verify := range []string{"", ", verify_data: false", ", verify_data: true"} {
		t.Run(verify, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			text := `version: 1
repositories:
  remote:
    location: "user@host:~/repo"
    passphrase_file: ./missing-secret
    identity_file: ./missing-key
workflows:
  integrity:
    cron: "@weekly"
    stages:
      - tasks: [{type: check, repository: remote, lock_wait: 0` + verify + `}]
`
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Prepare(selected, "integrity", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			zero := 0
			verifyData := strings.HasSuffix(verify, "true")
			want, err := check.Prepare(check.Options{
				Repository: "user@host:~/repo", LockWait: &zero, VerifyData: verifyData,
				PassphraseFile: filepath.Join(dir, "missing-secret"), IdentityFile: filepath.Join(dir, "missing-key"),
			}, dir)
			if err != nil {
				t.Fatal(err)
			}
			task := plan.Stages[0][0]
			if task.Type != "check" || task.Repository != "remote" || !reflect.DeepEqual(task.Request, want) {
				t.Fatal("check differs from standalone preparation")
			}
			for _, text := range []string{"whole repository consistency", "all archive metadata", "no repair", "Borg warnings"} {
				if !strings.Contains(task.Scope, text) {
					t.Errorf("scope missing %q", text)
				}
			}
			if verifyData {
				if !strings.Contains(task.Scope, "cryptographic file-content verification") || !strings.Contains(task.Scope, "substantial remote data") {
					t.Fatal("missing full verification scope/cost")
				}
			} else if !strings.Contains(task.Scope, "file contents are NOT cryptographically verified") {
				t.Fatal("default scope overstates content verification")
			}
		})
	}
}
