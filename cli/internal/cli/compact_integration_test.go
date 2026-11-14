package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestRealBorgCompactReclaimsSpace(t *testing.T) {
	for _, encryption := range []string{"none", "keyfile"} {
		t.Run(encryption, func(t *testing.T) {
			binary := realBorgEnvironment(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			t.Chdir(t.TempDir())
			cwd, _ := os.Getwd()
			repository := filepath.Join(cwd, "repo")
			const secret = "compact integration secret"
			borgOutput(t, ctx, binary, secret, "init", "--encryption="+encryption, repository)
			if err := os.Mkdir("source", 0700); err != nil {
				t.Fatal(err)
			}
			retained := make(map[string][]byte)
			// Unique uncompressed data makes actual reclamation measurable without
			// relying on Borg's diagnostic wording or exact segment layout.
			for _, prefix := range []string{"documents", "photos"} {
				for _, version := range []string{"old", "new"} {
					size, timestamp := 4<<20, "2020-01-01T10:00:00"
					if version == "new" {
						size, timestamp = 1<<20, "2020-01-02T10:00:00"
					}
					data := make([]byte, size)
					if _, err := rand.Read(data); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile("source/data", data, 0600); err != nil {
						t.Fatal(err)
					}
					name := prefix + "-" + version
					borgOutput(t, ctx, binary, secret, "create", "--compression=none", "--timestamp="+timestamp, "--", repository+"::"+name, "source")
					if version == "new" {
						retained[name] = data
					}
				}
			}
			for _, prefix := range []string{"documents", "photos"} {
				t.Run("prune "+prefix, func(t *testing.T) {
					t.Setenv("BORG_PASSPHRASE", secret)
					var stdout, stderr bytes.Buffer
					args := []string{"prune", "--repository", repository, "--archive-prefix", prefix, "--keep-secondly=1"}
					if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 {
						t.Fatalf("prune code = %d, stderr = %s", code, &stderr)
					}
				})
			}
			before := repositoryArchives(t, ctx, binary, repository, secret)
			assertArchiveNames(t, before, "documents-new", "photos-new")
			segmentBytes := repositorySegmentBytes(t, repository)
			// A keyfile repository requires a client-side key for archive access.
			// Compaction must work with a fresh, empty key directory and no secret.
			t.Run("keyless compaction", func(t *testing.T) {
				t.Setenv("BORG_KEYS_DIR", t.TempDir())
				t.Setenv("BORG_PASSCOMMAND", "/must-not-run-for-compaction")
				t.Setenv("BORG_REPO", "/must-not-be-used")
				if err := os.WriteFile("cairn.yaml", []byte("invalid: ["), 0600); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					var stdout, stderr bytes.Buffer
					if code := Run(ctx, []string{"compact", "--repository=repo"}, &stdout, &stderr, "test"); code != 0 ||
						stdout.String() != "Compact completed.\n" {
						t.Fatalf("compact code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
					}
					if strings.Contains(stdout.String()+stderr.String(), secret) {
						t.Fatal("secret leaked")
					}
				}
			})
			afterBytes := repositorySegmentBytes(t, repository)
			// Each prefix contributed 4 MiB of unique deleted data. Reclaiming
			// over 6 MiB demonstrates reclamation across both, allowing overhead.
			if segmentBytes-afterBytes < 6<<20 {
				t.Fatalf("insufficient reclamation: before %d, after %d", segmentBytes, afterBytes)
			}
			if after := repositoryArchives(t, ctx, binary, repository, secret); !reflect.DeepEqual(before, after) {
				t.Fatalf("compaction changed archives: before %v, after %v", before, after)
			}
			for name, want := range retained {
				got := borgOutput(t, ctx, binary, secret, "extract", "--stdout", repository+"::"+name, "source/data")
				if !bytes.Equal(got, want) {
					t.Fatalf("retained archive %q changed contents", name)
				}
			}
			borgOutput(t, ctx, binary, secret, "check", "--verify-data", repository)
		})
	}
}

func TestRealBorgCompactContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	t.Run("locked", func(t *testing.T) {
		holdRepository(t, ctx, binary, repository)
		var stdout, stderr bytes.Buffer
		args := []string{"compact", "--repository", repository, "--lock-wait=0"}
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 ||
			stdout.String() != "Compact failed.\n" || strings.Contains(stderr.String(), "for usage") {
			t.Fatalf("lock timeout code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
		stdout.Reset()
		stderr.Reset()
		waitingCtx, stop := context.WithCancelCause(ctx)
		defer stop(nil)
		args[3] = "--lock-wait=30"
		done := make(chan int, 1)
		go func() { done <- Run(waitingCtx, args, &stdout, &stderr, "test") }()
		awaitLockAttempt(t, repository)
		stop(cancellation.Signal(syscall.SIGTERM))
		select {
		case code := <-done:
			if code != 143 || stdout.String() != "Compact interrupted.\n" {
				t.Fatalf("waiting cancellation code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("waiting compact did not stop")
		}
	})
	// Lock holder and cancelled waiter have stopped; no lock bypass or deletion.
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"compact", "--repository", repository}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("subsequent compact code = %d, stderr = %s", code, &stderr)
	}
	assertArchiveNames(t, repositoryArchives(t, ctx, binary, repository, ""))
}

func TestRealBorgCompactRequiresExistingRepository(t *testing.T) {
	realBorgEnvironment(t)
	repository := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"compact", "--repository", repository}, &stdout, &stderr, "test"); code != 2 ||
		stdout.String() != "Compact failed.\n" {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	if _, err := os.Stat(repository); !os.IsNotExist(err) {
		t.Fatalf("compact created repository: %v", err)
	}
}
