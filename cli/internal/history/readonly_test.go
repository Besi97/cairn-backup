package history

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func TestReadOnlyMissingStorage(t *testing.T) {
	for _, depth := range []int{0, 1, 2, 3} {
		t.Run(strconv.Itoa(depth), func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			path := state
			for level := range depth {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				if level == 0 {
					path = filepath.Join(path, "cairn")
				} else {
					path = filepath.Join(path, "history")
				}
			}
			t.Setenv("XDG_STATE_HOME", state)
			s, err := OpenReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			runs, err := s.Inspect()
			if err != nil || len(runs) != 0 {
				t.Fatalf("%+v %v", runs, err)
			}
			if err := s.Observe(event("rejected", 1, lifecycle.RunStarted)); err != ErrUnavailable {
				t.Fatalf("read-only write: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.List(); err != ErrUnavailable {
				t.Fatalf("closed read: %v", err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("created missing path %s: %v", path, err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 0 {
				t.Fatalf("reader created files: %v %v", entries, err)
			}
		})
	}
}

func TestReadOnlySnapshotWithoutPersistenceLock(t *testing.T) {
	writer, dir := store(t)
	e := event("retained", 2, lifecycle.TaskFinished)
	e.Outcome = lifecycle.Warning
	if err := writer.Observe(e); err != nil {
		t.Fatal(err)
	}
	want, err := writer.List()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := writer.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	// A reader must succeed even while a writer owns the persistence lock.
	s, err := openStore(filepath.Dir(filepath.Dir(dir)), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.List()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read under lock: %+v %v", got, err)
	}
	if _, err := s.Inspect(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("reader changed snapshot")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "history.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "history.lock")); !os.IsNotExist(err) {
		t.Fatalf("reader created lock: %v", err)
	}
}

func TestReadOnlyUnsafeOrMalformedStorage(t *testing.T) {
	for _, kind := range []string{
		"state-mode", "directory-mode", "directory-symlink", "directory-file",
		"file-mode", "permission", "symlink", "dangling-symlink", "hardlink", "fifo",
		"malformed", "oversized", "version",
	} {
		t.Run(kind, func(t *testing.T) {
			_, dir := store(t)
			state := filepath.Dir(filepath.Dir(dir))
			path := filepath.Join(dir, "runs.json")
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "state-mode":
				must(os.Chmod(state, 0777))
			case "directory-mode":
				must(os.Chmod(dir, 0755))
			case "directory-symlink", "directory-file":
				must(os.Remove(dir))
				if kind == "directory-symlink" {
					must(os.Symlink(t.TempDir(), dir))
				} else {
					must(os.WriteFile(dir, nil, 0600))
				}
			case "file-mode", "permission":
				mode := os.FileMode(0644)
				if kind == "permission" {
					mode = 0000
				}
				must(os.WriteFile(path, nil, mode))
			case "symlink", "dangling-symlink":
				target := filepath.Join(t.TempDir(), "private-target")
				if kind == "symlink" {
					must(os.WriteFile(target, nil, 0600))
				}
				must(os.Symlink(target, path))
			case "hardlink":
				target := filepath.Join(t.TempDir(), "private-target")
				must(os.WriteFile(target, nil, 0600))
				must(os.Link(target, path))
			case "fifo":
				must(syscall.Mkfifo(path, 0600))
			case "malformed":
				must(os.WriteFile(path, []byte("private-invalid-payload"), 0600))
			case "oversized":
				must(os.WriteFile(path, bytes.Repeat([]byte("x"), MaxBytes/2+1), 0600))
			case "version":
				must(os.WriteFile(path, []byte(`{"version":2,"runs":[]}`), 0600))
			}
			s, err := openStore(state, true)
			if err == nil {
				defer s.Close()
				_, err = s.Inspect()
			}
			if err != ErrUnavailable {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestReadOnlyStateEnvironment(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative")
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := OpenReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatalf("created fallback storage: %v", err)
	}
	t.Setenv("HOME", "")
	if _, err := OpenReadOnly(); err != ErrUnavailable {
		t.Fatalf("invalid environment: %v", err)
	}
}

func TestReadOnlyConcurrentAtomicSnapshots(t *testing.T) {
	writer, dir := store(t)
	if err := writer.Observe(event("atomic", 1, lifecycle.RunStarted)); err != nil {
		t.Fatal(err)
	}
	reader, err := openStore(filepath.Dir(filepath.Dir(dir)), true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		for sequence := uint64(2); sequence <= 20; sequence++ {
			e := event("atomic", sequence, lifecycle.TaskFinished)
			e.Outcome = lifecycle.Success
			if err := writer.Observe(e); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	var readErr error
	for range 100 {
		runs, err := reader.List()
		if err != nil || len(runs) != 1 || runs[0].Tasks.Success != runs[0].Sequence-1 {
			readErr = fmt.Errorf("runs=%+v err=%v", runs, err)
			break
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatalf("reader saw an incomplete atomic snapshot: %v", readErr)
	}
}
