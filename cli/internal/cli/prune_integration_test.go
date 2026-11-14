package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestRealBorgPruneScopeAndRetention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	t.Setenv("TZ", "UTC")
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	repository := filepath.Join(cwd, "repo")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	if err := os.WriteFile("cairn.yaml", []byte("invalid: [ unrelated configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	fixtures := []archiveFixture{
		{"documents-20990101T000000.000000000Z", "2019-12-30T10:00:00"},
		{"documents-weekly", "2020-01-06T10:00:00"},
		{"documents-same-day", "2020-01-14T09:00:00"},
		{"documents-before-upgrade", "2020-01-14T10:00:00"},
		{"documents-old.checkpoint.1", "2020-01-14T11:00:00"},
		{"documents-latest.checkpoint", "2020-01-14T12:00:00"},
		{"documents2-unrelated", "2020-01-01T10:00:00"},
		{"documents", "2020-01-01T11:00:00"},
		{"other-before-upgrade", "2020-01-01T12:00:00"},
		{"other-latest.checkpoint", "2020-01-15T10:00:00"},
	}
	seedArchives(t, ctx, binary, repository, "", fixtures)
	before := repositoryArchives(t, ctx, binary, repository, "")
	args := []string{"prune", "--repository=repo", "--archive-prefix=documents", "--keep-daily=1", "--keep-weekly=1"}
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, append(args, "--dry-run"), &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("preview code = %d, stderr = %s", code, &stderr)
	}
	if stdout.String() != "Prune preview completed; no archives deleted.\n" {
		t.Fatalf("stdout = %q", &stdout)
	}
	for _, name := range []string{"documents-20990101T000000.000000000Z", "documents-same-day", "documents-old.checkpoint.1"} {
		pattern := regexp.MustCompile(`Would prune:\s+` + regexp.QuoteMeta(name) + `\s`)
		if !pattern.MatchString(stderr.String()) {
			t.Errorf("missing preview deletion for %q in %s", name, &stderr)
		}
	}
	for _, name := range []string{"documents-weekly", "documents-before-upgrade", "documents-latest.checkpoint"} {
		pattern := regexp.MustCompile(`Keeping[^\n]*\s` + regexp.QuoteMeta(name) + `\s`)
		if !pattern.MatchString(stderr.String()) {
			t.Errorf("missing preview retention for %q in %s", name, &stderr)
		}
	}
	for _, name := range []string{"documents2-unrelated", "other-before-upgrade", "other-latest.checkpoint"} {
		if strings.Contains(stderr.String(), name) {
			t.Errorf("out-of-scope archive %q appeared in prune list", name)
		}
	}
	if after := repositoryArchives(t, ctx, binary, repository, ""); !reflect.DeepEqual(before, after) {
		t.Fatalf("preview changed archive identities: before %v, after %v", before, after)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 || stdout.String() != "Prune completed.\n" {
		t.Fatalf("prune code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	after := repositoryArchives(t, ctx, binary, repository, "")
	assertArchiveNames(t, after, "documents-weekly", "documents-before-upgrade", "documents-latest.checkpoint",
		"documents2-unrelated", "documents", "other-before-upgrade", "other-latest.checkpoint")
	for name, id := range after {
		if id != before[name] {
			t.Errorf("retained archive %q changed identity", name)
		}
	}
	// Once a newer complete backup exists, Borg removes the old checkpoint too.
	seedArchives(t, ctx, binary, repository, "", []archiveFixture{{"documents-complete", "2020-01-15T10:00:00"}})
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("subsequent prune code = %d, stderr = %s", code, &stderr)
	}
	after = repositoryArchives(t, ctx, binary, repository, "")
	if _, ok := after["documents-latest.checkpoint"]; ok {
		t.Fatal("obsolete checkpoint survived a later completed backup")
	}
	if _, ok := after["other-latest.checkpoint"]; !ok {
		t.Fatal("out-of-scope checkpoint was removed")
	}
}

func TestRealBorgPruneSelectorsAndUnlimited(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	hostname, _ := os.Hostname()
	hostname = strings.Split(hostname, ".")[0]
	prefix := "-" + hostname
	fixtures := []archiveFixture{
		{prefix + "-a-old", "2020-01-01T10:00:00"},
		{prefix + "-b-new", "2020-01-02T10:00:00"},
		{prefix + "-c-outside", "2020-01-03T10:00:00"},
	}
	seedArchives(t, ctx, binary, repository, "", fixtures)
	args := []string{"prune", "--repository", repository, "--archive-prefix=-{hostname}-[ab]", "--keep-secondly=-1"}
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("unlimited code = %d, stderr = %s", code, &stderr)
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, ""), fixtures[0].name, fixtures[1].name, fixtures[2].name)
	args[len(args)-1] = "--keep-secondly=1"
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("glob code = %d, stderr = %s", code, &stderr)
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, ""), fixtures[1].name, fixtures[2].name)
	// A selector with no matches is a native no-op, not an implicit all-archives mode.
	stdout.Reset()
	stderr.Reset()
	args[3] = "--archive-prefix=missing"
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("no matches code = %d, stderr = %s", code, &stderr)
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, ""), fixtures[1].name, fixtures[2].name)
}

func TestRealBorgPruneWithin(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	now := time.Now().UTC()
	seedArchives(t, ctx, binary, repository, "", []archiveFixture{
		{"within-old", now.Add(-72 * time.Hour).Format("2006-01-02T15:04:05")},
		{"within-recent", now.Add(-time.Hour).Format("2006-01-02T15:04:05")},
	})
	var stdout, stderr bytes.Buffer
	args := []string{"prune", "--repository", repository, "--archive-prefix=within", "--keep-within=2d"}
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("within code = %d, stderr = %s", code, &stderr)
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, ""), "within-recent")
}

func TestRealBorgPruneTimezone(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	seedArchives(t, ctx, binary, repository, "", []archiveFixture{
		{"timezone-old", "2020-01-13T20:00:00"},
		{"timezone-middle", "2020-01-14T02:00:00"},
		{"timezone-new", "2020-01-14T14:00:00"},
	})
	before := repositoryArchives(t, ctx, binary, repository, "")
	for _, tt := range []struct{ timezone, removed string }{{"UTC", "timezone-middle"}, {"Etc/GMT+12", "timezone-old"}} {
		t.Run(tt.timezone, func(t *testing.T) {
			t.Setenv("TZ", tt.timezone)
			var stdout, stderr bytes.Buffer
			args := []string{"prune", "--repository", repository, "--archive-prefix=timezone", "--keep-daily=2", "--dry-run"}
			if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
				t.Fatalf("timezone code = %d, stderr = %s", code, &stderr)
			}
			if !regexp.MustCompile(`Would prune:\s+` + tt.removed + `\s`).MatchString(stderr.String()) {
				t.Fatalf("wrong local day selection: %s", &stderr)
			}
		})
	}
	if after := repositoryArchives(t, ctx, binary, repository, ""); !reflect.DeepEqual(before, after) {
		t.Fatal("timezone previews changed archives")
	}
}

func TestRealBorgPruneCredentials(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	repository := filepath.Join(t.TempDir(), "repo")
	const secret = "prune integration secret"
	borgOutput(t, ctx, binary, secret, "init", "--encryption=repokey", repository)
	seedArchives(t, ctx, binary, repository, secret, []archiveFixture{
		{"encrypted-old", "2020-01-01T10:00:00"}, {"encrypted-new", "2020-01-02T10:00:00"},
	})
	if err := os.WriteFile("passphrase", []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, preview := range []bool{true, false} {
		for _, mode := range []string{"missing", "wrong", "file"} {
			t.Run(fmt.Sprintf("%t/%s", preview, mode), func(t *testing.T) {
				args := []string{"prune", "--repository", repository, "--archive-prefix=encrypted", "--keep-secondly=1"}
				if preview {
					args = append(args, "--dry-run")
				}
				want := 2
				if mode == "file" {
					want = 0
					t.Setenv("BORG_PASSPHRASE", "ignored")
					t.Setenv("BORG_PASSCOMMAND", "must-not-run")
					t.Setenv("BORG_PASSPHRASE_FD", "invalid")
					args = append(args, "--passphrase-file=passphrase")
				} else if mode == "wrong" {
					t.Setenv("BORG_PASSPHRASE", "wrong")
				}
				var stdout, stderr bytes.Buffer
				if code := Run(ctx, args, &stdout, &stderr, "test"); code != want {
					t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
				}
				if strings.Contains(stdout.String()+stderr.String(), secret) || strings.Contains(stderr.String(), "Enter passphrase") {
					t.Fatal("secret leaked or interactive prompt attempted")
				}
			})
		}
		if preview {
			assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, secret), "encrypted-old", "encrypted-new")
		}
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, secret), "encrypted-new")

	// Block a real prune inside its trusted passcommand to deterministically
	// cancel an operation with owned subprocesses, without racing deletion.
	base := filepath.Join(t.TempDir(), "passcommand")
	t.Setenv("FAKE_TREE", base)
	t.Setenv("BORG_PASSCOMMAND", fakeBorg)
	runningCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(runningCtx, []string{"prune", "--repository", repository, "--archive-prefix=encrypted", "--keep-daily=1"}, &stdout, &stderr, "test")
	}()
	awaitFile(t, base+".parent")
	awaitFile(t, base+".child")
	stop(cancellation.Signal(syscall.SIGINT))
	select {
	case code := <-done:
		if code != 130 || stdout.String() != "Prune interrupted.\n" {
			t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("running prune did not stop")
	}
	for _, role := range []string{"parent", "child"} {
		data, err := os.ReadFile(base + "." + role + ".signal")
		if err != nil || string(data) != strconv.Itoa(int(syscall.SIGINT)) {
			t.Fatalf("%s received %s: %v", role, data, err)
		}
	}
	// Access succeeds after cancellation without removing or bypassing locks.
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, secret), "encrypted-new")
}

func TestRealBorgPruneContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	holdRepository(t, ctx, binary, repository)
	for _, preview := range []bool{false, true} {
		args := []string{"prune", "--repository", repository, "--archive-prefix=blocked", "--keep-daily=1", "--lock-wait=0"}
		if preview {
			args = append(args, "--dry-run")
		}
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || strings.Contains(stderr.String(), "for usage") {
			t.Fatalf("lock timeout code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
		stdout.Reset()
		stderr.Reset()
		waitingCtx, stop := context.WithCancelCause(ctx)
		defer stop(nil)
		args[5] = "--lock-wait=30"
		done := make(chan int, 1)
		go func() { done <- Run(waitingCtx, args, &stdout, &stderr, "test") }()
		awaitLockAttempt(t, repository)
		stop(cancellation.Signal(syscall.SIGTERM))
		select {
		case code := <-done:
			if code != 143 || !strings.Contains(stdout.String(), "interrupted") {
				t.Fatalf("waiting cancellation code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("waiting prune did not stop")
		}
	}
}
