package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestRealBorgCheck(t *testing.T) {
	for _, encryption := range []string{"none", "repokey"} {
		t.Run(encryption, func(t *testing.T) {
			binary := realBorgEnvironment(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			t.Chdir(t.TempDir())
			const secret = "check integration secret"
			borgOutput(t, ctx, binary, secret, "init", "--encryption="+encryption, "repo")
			if err := os.WriteFile("source", []byte("archived contents"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("passphrase", []byte(secret+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"documents-old", "photos-new"} {
				borgOutput(t, ctx, binary, secret, "create", "repo::"+name, "source")
			}
			before := repositoryArchives(t, ctx, binary, "repo", secret)
			for _, verify := range []bool{false, true} {
				args := []string{"check", "--repository=repo", "--passphrase-file=passphrase"}
				if verify {
					args = append(args, "--verify-data")
				}
				var stdout, stderr bytes.Buffer
				if code := Run(ctx, args, &stdout, &stderr, "test"); code != 0 || stdout.String() != "Check completed.\n" {
					t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
				}
				for _, text := range []string{"Starting repository check", "Starting archive consistency check", "documents-old", "photos-new"} {
					if !strings.Contains(stderr.String(), text) {
						t.Errorf("missing %q in diagnostics: %s", text, &stderr)
					}
				}
				if strings.Contains(stdout.String()+stderr.String(), secret) {
					t.Fatal("secret leaked")
				}
			}
			if after := repositoryArchives(t, ctx, binary, "repo", secret); !reflect.DeepEqual(before, after) {
				t.Fatalf("check changed archives: before %v, after %v", before, after)
			}
			for name := range before {
				if got := string(borgOutput(t, ctx, binary, secret, "extract", "--stdout", "repo::"+name, "source")); got != "archived contents" {
					t.Fatalf("check changed archive %s contents: %q", name, got)
				}
			}
			if encryption == "repokey" {
				for _, credential := range []string{"", "wrong"} {
					t.Run("invalid credentials "+credential, func(t *testing.T) {
						t.Setenv("BORG_PASSPHRASE", credential)
						var stdout, stderr bytes.Buffer
						if code := Run(ctx, []string{"check", "--repository=repo"}, &stdout, &stderr, "test"); code != 2 ||
							stdout.String() != "Check failed.\n" {
							t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
						}
						if strings.Contains(stdout.String()+stderr.String(), secret) || strings.Contains(stderr.String(), "Enter passphrase") {
							t.Fatal("secret leaked or interactive prompt attempted")
						}
					})
				}
			}
		})
	}
}

func TestRealBorgCheckDetectsDamageWithoutRepair(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	segments, err := filepath.Glob(filepath.Join(repository, "data", "*", "*"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("repository segments = %v: %v", segments, err)
	}
	segment := segments[0]
	damaged, err := os.ReadFile(segment)
	if err != nil || len(damaged) == 0 {
		t.Fatalf("read segment: %v", err)
	}
	damaged[0] ^= 0xff
	if err := os.WriteFile(segment, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	for _, verify := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		args := []string{"check", "--repository", repository}
		if verify {
			args = append(args, "--verify-data")
		}
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || stdout.String() != "Check failed.\n" ||
			!strings.Contains(stderr.String(), "Invalid segment magic") {
			t.Fatalf("damage code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
		after, err := os.ReadFile(segment)
		if err != nil || !bytes.Equal(damaged, after) {
			t.Fatalf("check modified damaged segment: %v", err)
		}
	}
}

func TestRealBorgCheckContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	t.Run("locked", func(t *testing.T) {
		holdRepository(t, ctx, binary, repository)
		var stdout, stderr bytes.Buffer
		args := []string{"check", "--repository", repository, "--lock-wait=0"}
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 ||
			stdout.String() != "Check failed.\n" || strings.Contains(stderr.String(), "for usage") {
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
			if code != 143 || stdout.String() != "Check interrupted.\n" {
				t.Fatalf("waiting cancellation code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("waiting check did not stop")
		}
	})
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"check", "--repository", repository}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("subsequent check code = %d, stderr = %s", code, &stderr)
	}
}

func TestRealBorgCheckRunningCancellation(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	const secret = "check cancellation secret"
	borgOutput(t, ctx, binary, secret, "init", "--encryption=repokey", repository)
	base := filepath.Join(t.TempDir(), "passcommand")
	t.Setenv("FAKE_TREE", base)
	t.Setenv("BORG_PASSCOMMAND", fakeBorg)
	runningCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(runningCtx, []string{"check", "--repository", repository, "--verify-data"}, &stdout, &stderr, "test")
	}()
	awaitFile(t, base+".parent")
	awaitFile(t, base+".child")
	stop(cancellation.Signal(syscall.SIGINT))
	select {
	case code := <-done:
		if code != 130 || stdout.String() != "Check interrupted.\n" {
			t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("running check did not stop")
	}
	t.Setenv("BORG_PASSPHRASE", secret)
	t.Setenv("BORG_PASSCOMMAND", "")
	stdout.Reset()
	stderr.Reset()
	if code := Run(ctx, []string{"check", "--repository", repository}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("subsequent check code = %d, stderr = %s", code, &stderr)
	}
}

func TestRealBorgCheckRequiresExistingRepository(t *testing.T) {
	realBorgEnvironment(t)
	repository := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"check", "--repository", repository}, &stdout, &stderr, "test"); code != 2 ||
		stdout.String() != "Check failed.\n" {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
	}
	if _, err := os.Stat(repository); !os.IsNotExist(err) {
		t.Fatalf("check created repository: %v", err)
	}
}
