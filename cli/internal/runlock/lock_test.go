package runlock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func identity(t *testing.T) Identity {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := Config(path)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func privateTemp(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIdentityAndNamespaces(t *testing.T) {
	id := identity(t)
	link := filepath.Join(t.TempDir(), "alias.yaml")
	if err := os.Symlink(id.path, link); err != nil {
		t.Fatal(err)
	}
	alias, err := Config(link)
	if err != nil || alias != id {
		t.Fatalf("alias identity = %v, %v", alias, err)
	}
	store, err := open(privateTemp(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(id, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := store.Workflow(alias, "nightly"); !errors.Is(err, ErrBusy) {
		t.Fatalf("duplicate = %v", err)
	}
	for _, acquire := range []func() (*os.File, error){
		func() (*os.File, error) { return store.Workflow(id, "other") },
		func() (*os.File, error) { return store.Workflow(identity(t), "nightly") },
		func() (*os.File, error) { return store.Agent(id) },
	} {
		file, err := acquire()
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	before, err := lock.Stat()
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	next, err := store.Workflow(id, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	after, err := next.Stat()
	next.Close()
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("release replaced lock file")
	}
	agent, err := store.Agent(id)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	if _, err := store.Agent(alias); !errors.Is(err, ErrBusy) {
		t.Fatalf("second agent = %v", err)
	}
	if _, err := store.Agent(Identity{}); err == nil {
		t.Fatal("empty identity accepted")
	}
}

func TestDirectorySelection(t *testing.T) {
	for _, mode := range []string{"runtime", "unset", "relative", "unsafe", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			runtime, state := privateTemp(t), t.TempDir()
			original := runtime
			switch mode {
			case "unset":
				runtime = ""
			case "relative":
				runtime = "relative"
			case "unsafe":
				if err := os.Chmod(runtime, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				runtime = filepath.Join(t.TempDir(), "runtime")
				if err := os.Symlink(original, runtime); err != nil {
					t.Fatal(err)
				}
			}
			store, err := open(runtime, state, "")
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			base := state
			if mode == "runtime" {
				base = runtime
			}
			actual, err := os.Readlink(filepath.Join("/proc/self/fd", strconv.Itoa(int(store.directory.Fd()))))
			if err != nil || actual != filepath.Join(base, "cairn", "locks") {
				t.Fatalf("directory = %s, %v", actual, err)
			}
		})
	}
	home := t.TempDir()
	store, err := open("", "", home)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if _, err := os.Stat(filepath.Join(home, ".local/state/cairn/locks")); err != nil {
		t.Fatal(err)
	}
	if _, err := open("", "", ""); err == nil {
		t.Fatal("missing home accepted")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0777); err != nil {
		t.Fatal(err)
	}
	if store, err := open("", state, ""); err == nil {
		store.Close()
		t.Fatal("world-writable state directory accepted")
	}
}

func TestUnsafePaths(t *testing.T) {
	for _, mode := range []string{"directory permissions", "directory symlink", "file symlink", "file permissions", "hardlink", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			runtime := privateTemp(t)
			if mode == "directory permissions" {
				if err := os.Mkdir(filepath.Join(runtime, "cairn"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "directory symlink" {
				if err := os.Symlink(t.TempDir(), filepath.Join(runtime, "cairn")); err != nil {
					t.Fatal(err)
				}
			}
			store, err := open(runtime, "", "")
			if mode == "directory permissions" || mode == "directory symlink" {
				if err == nil {
					store.Close()
					t.Fatal("unsafe directory accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			id := identity(t)
			lock, err := store.Agent(id)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(runtime, "cairn", "locks", lock.Name())
			lock.Close()
			var setupErr error
			switch mode {
			case "file symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				setupErr = os.Symlink(filepath.Join(t.TempDir(), "target"), path)
			case "file permissions":
				setupErr = os.Chmod(path, 0644)
			case "hardlink":
				setupErr = os.Link(path, filepath.Join(t.TempDir(), "alias"))
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				setupErr = syscall.Mkfifo(path, 0600)
			}
			if setupErr != nil {
				t.Fatal(setupErr)
			}
			if file, err := store.Agent(id); err == nil {
				file.Close()
				t.Fatal("unsafe lock file accepted")
			}
		})
	}
}
