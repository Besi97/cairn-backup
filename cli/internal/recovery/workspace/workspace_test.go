package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

const regularListing = "-\x00-rw-------\x00"

type fixture struct {
	dir, temp, calls string
	runner           borg.Runner
	options          Options
}

func fakeBorg(t *testing.T, listing string, headers []*tar.Header, export, extract string) fixture {
	t.Helper()
	dir, temp := t.TempDir(), t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("TMPDIR", temp)
	t.Setenv("FIXTURE", dir)
	t.Setenv("BORG_PASSPHRASE", "")
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	for _, header := range headers {
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string, mode fs.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("listing", listing, 0600)
	write("tar", data.String(), 0600)
	write("borg", `#!/bin/sh
set -eu
if [ "$1" = "--version" ]; then echo "borg 1.4.5"; exit; fi
printf '%s\n' "$@" >> "$FIXTURE/calls"
case "$1" in
    list) cat "$FIXTURE/listing";;
    export-tar) `+export+`
        cat "$FIXTURE/tar";;
    extract) `+extract+`
        ;;
    *) exit 9;;
esac
`, 0700)
	return fixture{dir: dir, temp: temp, calls: calls, runner: borg.Runner{Executable: filepath.Join(dir, "borg")}, options: Options{
		Repository: borg.PreparedRepository{Location: filepath.Join(dir, "repo")}, ArchiveName: "snapshot",
		Paths: []string{"file"},
	}}
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("workspace remains: %v, %v", entries, err)
	}
}

func unexpectedConsumer(t *testing.T) func(context.Context, string) error {
	t.Helper()
	return func(context.Context, string) error {
		t.Error("consumer ran without a clean extraction")
		return nil
	}
}

func TestRunSelectionAndCleanup(t *testing.T) {
	f := fakeBorg(t, regularListing, []*tar.Header{{Name: "file"}}, ":", `
        printf '%s' restored > file
        chmod 0777 .
        mkdir locked
        printf '%s' private > locked/file
        ln -s "$FIXTURE" locked/outside
        chmod 000 locked`)
	wait := 0
	f.options.LockWait = &wait
	f.options.Paths = []string{"-option", "sh:literal*"}
	checkErr := errors.New("check failed")
	if err := os.Chmod(f.dir, 0750); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := Run(t.Context(), f.runner, f.options, io.Discard, func(_ context.Context, path string) error {
		called = true
		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("private workspace mode = %v", info.Mode())
		}
		if data, err := os.ReadFile(filepath.Join(path, "file")); err != nil || string(data) != "restored" {
			t.Fatalf("restored data = %q, %v", data, err)
		}
		return checkErr
	})
	if !called || result.Outcome != borg.Success || !errors.Is(err, checkErr) {
		t.Fatalf("called = %v, result = %+v, error = %v", called, result, err)
	}
	var cleanup *CleanupError
	if errors.As(err, &cleanup) {
		t.Fatal(cleanup)
	}
	assertEmpty(t, f.temp)
	info, err := os.Stat(f.dir)
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatalf("cleanup affected outside directory: %v, %v", info, err)
	}
	calls, err := os.ReadFile(f.calls)
	if err != nil {
		t.Fatal(err)
	}
	location := f.options.Repository.Location + "::snapshot"
	want := "list\n--lock-wait\n0\n--format\n{type}{NUL}{mode}{NUL}\n--\n" + location + "\npp:-option\npp:sh:literal*\n" +
		"export-tar\n--lock-wait\n0\n--\n" + location + "\n-\npp:-option\npp:sh:literal*\n" +
		"extract\n--lock-wait\n0\n--list\n--\n" + location + "\npp:-option\npp:sh:literal*\n"
	if string(calls) != want {
		t.Fatalf("Borg calls = %q, want %q", calls, want)
	}
}

func TestRunRejectsUnsafeInput(t *testing.T) {
	f := fakeBorg(t, "", nil, "exit 97", "exit 97")
	for _, test := range []struct {
		name   string
		mutate func(*Options)
	}{
		{"implicit whole archive", func(o *Options) { o.Paths = nil }},
		{"both scopes", func(o *Options) { o.WholeArchive = true }},
		{"unsafe selector", func(o *Options) { o.Paths = []string{"link/../outside"} }},
		{"absolute selector", func(o *Options) { o.Paths = []string{"/etc"} }},
		{"placeholder archive", func(o *Options) { o.ArchiveName = "{now}" }},
		{"archive separator", func(o *Options) { o.ArchiveName = "other::archive" }},
		{"local repository overlap", func(o *Options) { o.Repository.Location = f.temp }},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := f.options
			test.mutate(&opts)
			if result, err := Run(t.Context(), f.runner, opts, nil, unexpectedConsumer(t)); err == nil || result.Outcome != borg.Failure {
				t.Fatalf("accepted unsafe options: %+v, %v", result, err)
			}
			if _, err := os.Stat(f.calls); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("unexpected Borg call: %v", err)
			}
			assertEmpty(t, f.temp)
		})
	}
}

func TestRunExtractionFailures(t *testing.T) {
	for _, test := range []struct {
		name, archivePath, export, extract, want string
		outcome                                  borg.Outcome
	}{
		{"unsafe archive", "../outside", ":", "exit 97", "unsafe exported path", borg.Failure},
		{"preflight warning", "file", `cat "$FIXTURE/tar"; exit 1`, "exit 97", "preflight reported warnings", borg.Warning},
		{"partial failure", "file", ":", "touch partial; exit 2", "Borg exited with code 2", borg.Failure},
		{"partial warning", "file", ":", "touch partial; exit 1", "extraction reported warnings", borg.Warning},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := fakeBorg(t, regularListing, []*tar.Header{{Name: test.archivePath}}, test.export, test.extract)
			result, err := Run(t.Context(), f.runner, f.options, nil, unexpectedConsumer(t))
			if err == nil || !strings.Contains(err.Error(), test.want) || result.Outcome != test.outcome {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if test.extract == "exit 97" {
				data, err := os.ReadFile(f.calls)
				if err != nil || strings.Contains(string(data), "\nextract\n") {
					t.Fatalf("extraction attempted after failed preflight: %q, %v", data, err)
				}
			}
			assertEmpty(t, f.temp)
		})
	}
}

func TestRunCancellationWaitsForOwnedDescendant(t *testing.T) {
	f := fakeBorg(t, "", nil, ":", `
        touch partial
        (
            trap 'touch "$FIXTURE/stopping"; while [ ! -e "$FIXTURE/release" ]; do sleep .01; done; test -f partial && touch "$FIXTURE/stopped"; exit' TERM
            touch "$FIXTURE/started"
            while :; do sleep .01; done
        ) &
        wait`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	var result borg.Result
	var runErr error
	go func() {
		defer close(done)
		result, runErr = Run(ctx, f.runner, f.options, nil, unexpectedConsumer(t))
	}()
	release := func() {
		if err := os.WriteFile(filepath.Join(f.dir, "release"), nil, 0600); err != nil {
			t.Error(err)
		}
	}
	defer func() {
		release()
		cancel()
		<-done
	}()
	waitFor := func(name string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(filepath.Join(f.dir, name)); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s", name)
			case <-done:
				t.Fatalf("Run returned before %s: %+v, %v", name, result, runErr)
			case <-ticker.C:
			}
		}
	}
	waitFor("started")
	cancel()
	waitFor("stopping")
	entries, err := os.ReadDir(f.temp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("workspace removed while the descendant was stopping: %v, %v", entries, err)
	}
	release()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("Run did not return after descendant shutdown")
	}
	if result.Outcome != borg.Interrupted || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("result = %+v, error = %v", result, runErr)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "stopped")); err != nil {
		t.Fatalf("descendant could not access workspace during shutdown: %v", err)
	}
	assertEmpty(t, f.temp)
}

func TestRunCancellationDuringConsumer(t *testing.T) {
	f := fakeBorg(t, "", nil, ":", "touch file")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := Run(ctx, f.runner, f.options, nil, func(ctx context.Context, path string) error {
		cancel()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("consumer did not receive cancellation")
		}
		if _, err := os.Stat(filepath.Join(path, "file")); err != nil {
			t.Fatal("workspace unavailable before consumer returned:", err)
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("missing cancellation: %v", err)
	}
	assertEmpty(t, f.temp)
}

func TestRunSurfacesCleanupFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove a workspace even from a read-only directory")
	}
	f := fakeBorg(t, "", nil, ":", "touch file")
	t.Cleanup(func() {
		if err := os.Chmod(f.temp, 0700); err != nil {
			t.Error(err)
		}
	})
	checkErr := errors.New("check failed")
	var private string
	_, err := Run(t.Context(), f.runner, f.options, nil, func(_ context.Context, path string) error {
		private = filepath.Dir(path)
		if err := os.Chmod(f.temp, 0500); err != nil {
			t.Fatal(err)
		}
		return checkErr
	})
	var cleanup *CleanupError
	if !errors.Is(err, checkErr) || !errors.As(err, &cleanup) ||
		cleanup.Path != private || !errors.Is(cleanup, fs.ErrPermission) {
		t.Fatalf("cleanup failure did not retain both errors and workspace path: %v", err)
	}
}
