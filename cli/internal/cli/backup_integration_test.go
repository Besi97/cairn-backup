package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestRealBorgBackup(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	cwd, _ := os.Getwd()
	repository := filepath.Join(cwd, "repo")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	for _, dir := range []string{"sources/tree/a,b", "sources/tree/empty"} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, contents := range map[string]string{
		"sources/tree/keep.txt": "backup contents\n", "sources/tree/skip.tmp": "excluded", "sources/tree/a,b/skip": "excluded",
		"sources/-hyphen": "hyphen contents", "sources/-ignored": "excluded", "absolute.txt": "absolute contents", "cairn.yaml": "invalid: [ unrelated configuration",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hostname, _ := os.Hostname()
	hostname = strings.Split(hostname, ".")[0]
	expandedName := hostname + "-Before Upgrade"
	args := []string{"backup", "--repository", "repo", "--archive-name", "{hostname}-Before Upgrade", "--source-root", "sources",
		"--exclude", "*.tmp", "--exclude", "sh:tree/a,b/*", "--exclude=-ignored", "--", "./tree/../tree", "-hyphen", "-ignored", filepath.Join(cwd, "absolute.txt")}
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	if stdout.String() != "Backup completed.\n" {
		t.Fatalf("stdout = %q", &stdout)
	}
	files := json.NewDecoder(bytes.NewReader(borgOutput(t, ctx, binary, "", "list", "--json-lines", repository+"::"+expandedName)))
	var paths []string
	for {
		var file struct{ Path string }
		if err := files.Decode(&file); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	wantPaths := []string{"tree", "tree/keep.txt", "tree/a,b", "tree/empty", "-hyphen", strings.TrimPrefix(filepath.Join(cwd, "absolute.txt"), "/")}
	sort.Strings(wantPaths)
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("archive paths = %q, want %q", paths, wantPaths)
	}
	if got := string(borgOutput(t, ctx, binary, "", "extract", "--stdout", repository+"::"+expandedName, "tree/keep.txt")); got != "backup contents\n" {
		t.Fatalf("archived contents = %q", got)
	}
	// Collision is a native failure, not a replacement or automatic rename.
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || stdout.String() != "Backup failed.\n" {
		t.Fatalf("collision code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	prefixArgs := []string{"backup", "--repository", "repo", "--archive-prefix", "{hostname}-Mixed Prefix", "--source-root", "sources", "tree/keep.txt"}
	if code := Run(ctx, prefixArgs, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("prefix code = %d, stderr = %s", code, &stderr)
	}
	var archives struct {
		Archives []struct{ Name string }
	}
	if err := json.Unmarshal(borgOutput(t, ctx, binary, "", "list", "--json", repository), &archives); err != nil {
		t.Fatal(err)
	}
	if len(archives.Archives) != 2 {
		t.Fatalf("archives = %+v", archives)
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(hostname) + `-Mixed Prefix-\d{8}T\d{6}\.\d{9}Z$`)
	matched := false
	for _, archive := range archives.Archives {
		matched = matched || pattern.MatchString(archive.Name)
	}
	if !matched {
		t.Fatalf("generated archive name not found: %+v", archives)
	}
	// A missing source is Borg's completed-with-warnings outcome in both modes.
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, []string{"backup", "--repository", repository, "--archive-name", "warning", "sources/tree/keep.txt", "missing-source"}, &stdout, &stderr, "test"); code != 1 || stdout.String() != "Backup completed with warnings.\n" || !strings.Contains(stderr.String(), "missing-source") {
		t.Fatalf("warning code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
}

func TestRealBorgBackupPreview(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	if err := os.WriteFile("included", []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("excluded.tmp", []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "does-not-exist")
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"backup", "--repository", repository, "--archive-name", "preview", "--dry-run", "--exclude", "*.tmp", "included", "excluded.tmp"}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	if stdout.String() != "Backup preview completed; no archive created.\n" || !strings.Contains(stderr.String(), "included") || !strings.Contains(stderr.String(), "excluded.tmp") {
		t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
	}
	if _, err := os.Stat(repository); !os.IsNotExist(err) {
		t.Fatal("dry-run created a repository")
	}
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, []string{"backup", "--repository", repository, "--archive-name", "preview", "--dry-run", "included"}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, &stderr)
	}
	if out := borgOutput(t, ctx, binary, "", "list", "--short", repository); len(out) != 0 {
		t.Fatalf("dry-run created archives: %s", out)
	}
}

func TestRealBorgBackupCredentials(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	repository := filepath.Join(t.TempDir(), "repo")
	const secret = "backup integration secret"
	borgOutput(t, ctx, binary, secret, "init", "--encryption=repokey", repository)
	if err := os.Mkdir("sources", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("sources/file", []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("passphrase", []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"file", "environment", "command", "descriptor", "missing", "wrong"} {
		t.Run(mode, func(t *testing.T) {
			args := []string{"backup", "--repository", repository, "--archive-name", mode, "--source-root", "sources", "file"}
			want := 2
			if mode == "file" {
				want = 0
				t.Setenv("BORG_PASSPHRASE", "ignored")
				t.Setenv("BORG_PASSCOMMAND", "must-not-run")
				t.Setenv("BORG_PASSPHRASE_FD", "invalid")
				args = append(args, "--passphrase-file", "passphrase")
			} else if mode == "wrong" {
				t.Setenv("BORG_PASSPHRASE", "wrong")
			} else if mode == "environment" {
				want = 0
				t.Setenv("BORG_PASSPHRASE", secret)
			} else if mode == "command" {
				want = 0
				path, _ := filepath.Abs("passphrase")
				t.Setenv("BORG_PASSCOMMAND", "cat '"+strings.ReplaceAll(path, "'", "'\"'\"'")+"'")
			} else if mode == "descriptor" {
				want = 0
				file, err := os.Open("passphrase")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				t.Setenv("BORG_PASSPHRASE_FD", strconv.FormatUint(uint64(file.Fd()), 10))
			}
			var stdout, stderr bytes.Buffer
			if code := Run(ctx, args, &stdout, &stderr, "test"); code != want {
				t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), secret) || strings.Contains(stderr.String(), "Enter passphrase") {
				t.Fatal("secret leaked or interactive credential prompt attempted")
			}
		})
	}
	if got := string(borgOutput(t, ctx, binary, secret, "extract", "--stdout", repository+"::file", "file")); got != "contents" {
		t.Fatalf("encrypted backup contents = %q", got)
	}

	// A real Borg create blocked inside its trusted passcommand gives a
	// deterministic running operation without racing a large-file backup.
	base := filepath.Join(t.TempDir(), "passcommand")
	t.Setenv("FAKE_TREE", base)
	t.Setenv("BORG_PASSCOMMAND", fakeBorg)
	runningCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(runningCtx, []string{"backup", "--repository", repository, "--archive-name", "interrupted", "sources/file"}, &stdout, &stderr, "test")
	}()
	awaitFile(t, base+".parent")
	awaitFile(t, base+".child")
	stop(cancellation.Signal(syscall.SIGINT))
	select {
	case code := <-done:
		if code != 130 || stdout.String() != "Backup interrupted.\n" {
			t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("running backup did not stop")
	}
}

func TestRealBorgBackupContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	dir := t.TempDir()
	repository, source := filepath.Join(dir, "repo"), filepath.Join(dir, "source")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	if err := os.WriteFile(source, []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	holdRepository(t, ctx, binary, repository)
	var stdout, stderr bytes.Buffer
	args := []string{"backup", "--repository", repository, "--archive-name", "blocked", "--lock-wait", "0", source}
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || stdout.String() != "Backup failed.\n" || strings.Contains(stderr.String(), "for usage") {
		t.Fatalf("lock timeout code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	waitingCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	args[len(args)-2] = "30"
	done := make(chan int, 1)
	go func() { done <- Run(waitingCtx, args, &stdout, &stderr, "test") }()
	awaitLockAttempt(t, repository)
	stop(cancellation.Signal(syscall.SIGTERM))
	select {
	case code := <-done:
		if code != 143 || stdout.String() != "Backup interrupted.\n" {
			t.Fatalf("waiting cancellation code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("waiting backup did not stop")
	}
}
