package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestRealBorgArchiveListingAndInspection(t *testing.T) {
	for _, encryption := range []string{"none", "repokey"} {
		t.Run(encryption, func(t *testing.T) {
			binary := realBorgEnvironment(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			t.Chdir(t.TempDir())
			dir, _ := os.Getwd()
			repository := filepath.Join(dir, "repo")
			const secret = "listing integration secret"
			borgOutput(t, ctx, binary, secret, "init", "--encryption="+encryption, repository)
			if err := os.MkdirAll("tree/empty", 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"file", "spaces and 世界", "line\n\x1b[31mname", "nontext?", "nontext\xff"} {
				if err := os.WriteFile(filepath.Join("tree", name), []byte("contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("line\n\x1b[31mname", "tree/link"); err != nil {
				t.Fatal(err)
			}
			if err := os.Link("tree/file", "tree/hardlink"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("passphrase", []byte(secret+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"before upgrade", "backup.checkpoint"} {
				borgOutput(t, ctx, binary, secret, "create", "--", repository+"::"+name, "tree")
			}
			before := repositoryArchives(t, ctx, binary, repository, secret)
			t.Setenv("BORG_PASSPHRASE", "wrong inherited value")
			t.Setenv("BORG_REPO", "/must-not-be-used")
			if err := os.WriteFile("cairn.yaml", []byte("invalid: ["), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			list := []string{"archive", "list", "--repository=repo", "--passphrase-file=passphrase", "--json"}
			if code := Run(ctx, list, &stdout, &stderr, "test"); code != 0 {
				t.Fatalf("list code = %d, stderr = %s", code, &stderr)
			}
			decoder := json.NewDecoder(&stdout)
			listed := make(map[string]string)
			for {
				var entry archive.Archive
				if err := decoder.Decode(&entry); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				listed[string(entry.Name)] = entry.ID
			}
			if !reflect.DeepEqual(listed, before) {
				t.Fatalf("listed = %v, want %v (including checkpoints)", listed, before)
			}
			stdout.Reset()
			stderr.Reset()
			inspect := []string{"archive", "inspect", "--repository=repo", "--archive-name=before upgrade", "--passphrase-file=passphrase", "--json"}
			if code := Run(ctx, inspect, &stdout, &stderr, "test"); code != 0 {
				t.Fatalf("inspect code = %d, stderr = %s", code, &stderr)
			}
			files := decodeArchiveFileRows(t, stdout.Bytes())
			native := borgOutput(t, ctx, binary, secret, "list", "--json-lines", "--format", "{size}{mtime}", repository+"::before upgrade")
			if want := decodeArchiveFileRows(t, native); !reflect.DeepEqual(files, want) {
				t.Fatalf("files = %+v, native = %+v", files, want)
			}
			displayCollisions, links, directories := 0, 0, 0
			for _, file := range files {
				if file.Path == "tree/nontext?" {
					displayCollisions++
				}
				if file.Type == "l" {
					links++
					if file.LinkTarget != "line\n\x1b[31mname" {
						t.Fatalf("link target = %q", file.LinkTarget)
					}
				}
				if file.Type == "d" {
					directories++
				}
			}
			if displayCollisions != 2 || links != 1 || directories != 2 {
				t.Fatalf("display collisions = %d, links = %d, directories = %d", displayCollisions, links, directories)
			}
			stdout.Reset()
			stderr.Reset()
			if code := Run(ctx, inspect[:len(inspect)-1], &stdout, &stderr, "test"); code != 0 ||
				strings.ContainsRune(stdout.String(), '\x1b') || !strings.Contains(stdout.String(), `\n\x1b[31mname`) {
				t.Fatalf("human inspection code = %d, stdout = %q, stderr = %s", code, &stdout, &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), secret) {
				t.Fatal("secret leaked")
			}
			if after := repositoryArchives(t, ctx, binary, repository, secret); !reflect.DeepEqual(before, after) {
				t.Fatalf("archives changed: before %v, after %v", before, after)
			}
			borgOutput(t, ctx, binary, secret, "check", "--verify-data", repository)
		})
	}
}

func decodeArchiveFileRows(t *testing.T, data []byte) []archive.File {
	t.Helper()
	var result []archive.File
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var file archive.File
		if err := decoder.Decode(&file); err == io.EOF {
			return result
		} else if err != nil {
			t.Fatal(err)
		}
		result = append(result, file)
	}
}

func TestRealBorgArchiveFailuresAndContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	for _, args := range [][]string{
		{"archive", "list", "--repository=" + filepath.Join(t.TempDir(), "missing")},
		{"archive", "inspect", "--repository=" + repository, "--archive-name=missing"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || stdout.Len() != 0 {
			t.Fatalf("code = %d, stdout = %q, stderr = %s", code, &stdout, &stderr)
		}
	}
	t.Run("locked", func(t *testing.T) {
		holdRepository(t, ctx, binary, repository)
		for _, operation := range []string{"list", "inspect"} {
			args := []string{"archive", operation, "--repository=" + repository, "--lock-wait=0"}
			if operation == "inspect" {
				args = append(args, "--archive-name=missing")
			}
			var stdout, stderr bytes.Buffer
			if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || stdout.Len() != 0 {
				t.Fatalf("lock timeout code = %d, stdout = %q, stderr = %s", code, &stdout, &stderr)
			}
		}
		waitCtx, stop := context.WithCancelCause(ctx)
		defer stop(nil)
		var stdout, stderr bytes.Buffer
		done := make(chan int, 1)
		go func() {
			done <- Run(waitCtx, []string{"archive", "list", "--repository=" + repository, "--lock-wait=30"}, &stdout, &stderr, "test")
		}()
		awaitLockAttempt(t, repository)
		stop(cancellation.Signal(syscall.SIGTERM))
		select {
		case code := <-done:
			if code != 143 || stdout.Len() != 0 {
				t.Fatalf("cancel code = %d, stdout = %q, stderr = %s", code, &stdout, &stderr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("listing did not stop")
		}
	})
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"archive", "list", "--repository=" + repository}, &stdout, &stderr, "test"); code != 0 || stdout.Len() != 0 {
		t.Fatalf("subsequent empty list code = %d, stdout = %q, stderr = %s", code, &stdout, &stderr)
	}
}
