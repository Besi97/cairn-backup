package runlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inspectionLock(t *testing.T) (*os.File, *Evidence) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", privateTemp(t))
	t.Setenv("XDG_STATE_HOME", privateTemp(t))
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lock, err := s.Workflow(identity(t), "nightly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	e := Capture(lock)
	if e == nil || !e.Valid() {
		t.Fatal("missing execution evidence")
	}
	return lock, e
}

func TestInspectionIsNonintrusive(t *testing.T) {
	lock, e := inspectionLock(t)
	for range 10 {
		i := NewInspector()
		o := i.Inspect(e)
		i.Close()
		if o.Owner != OwnerPresent || o.Lock != LockHeld {
			t.Fatalf("%+v", o)
		}
	}
	i := NewInspector()
	i.boot = "00000000-0000-0000-0000-000000000000"
	if o := i.Inspect(e); o.Owner != OwnerGone || o.Lock != LockHeld {
		t.Fatalf("owner identity and held lock were conflated: %+v", o)
	}
	i.Close()
	lock.Close()
	i = NewInspector()
	defer i.Close()
	if o := i.Inspect(e); o.Owner != OwnerPresent || o.Lock != LockUnknown {
		t.Fatalf("absence implied release: %+v", o)
	}
	// An empty kernel view cannot establish release, even for a known live owner.
	i.locks = []string{""}
	if o := i.Inspect(e); o.Lock != LockUnknown {
		t.Fatal(o)
	}
	for _, view := range [][]string{nil, {"malformed"}, {"1: FLOCK ADVISORY WRITE 1 invalid 0 EOF"}} {
		i.locks = view
		if o := i.Inspect(e); o.Lock != LockUnknown {
			t.Fatal(o)
		}
	}
}

func TestOwnerIdentity(t *testing.T) {
	_, evidence := inspectionLock(t)
	for _, tt := range []struct {
		name, boot, state string
		ticks             uint64
		err               error
		want              OwnerState
	}{
		{"same owner", evidence.BootID, "S", evidence.StartTicks, nil, OwnerPresent},
		{"reused PID", evidence.BootID, "S", evidence.StartTicks + 1, nil, OwnerGone},
		{"zombie", evidence.BootID, "Z", evidence.StartTicks, nil, OwnerGone},
		{"missing PID", evidence.BootID, "", 0, os.ErrNotExist, OwnerGone},
		{"permission", evidence.BootID, "", 0, os.ErrPermission, OwnerUnknown},
		{"unreadable", evidence.BootID, "", 0, errors.New("private error"), OwnerUnknown},
		{"missing boot", "", "S", evidence.StartTicks, nil, OwnerUnknown},
		{"restart", "00000000-0000-0000-0000-000000000000", "S", evidence.StartTicks, nil, OwnerGone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := observeOwner(*evidence, tt.boot, func(pid int) (uint64, string, error) {
				if pid != evidence.PID {
					t.Fatal(pid)
				}
				return tt.ticks, tt.state, tt.err
			})
			if got != tt.want {
				t.Fatalf("%s, want %s", got, tt.want)
			}
		})
	}
}

func TestInspectionMissingAndUnsafeEvidence(t *testing.T) {
	lock, e := inspectionLock(t)
	path := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cairn", "locks", lock.Name())
	for _, mode := range []string{"permissions", "symlink", "hardlink", "missing", "missing directory"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "permissions":
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0600)
			case "symlink", "hardlink":
				alias := filepath.Join(filepath.Dir(path), "alias")
				if err := os.Rename(path, alias); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(alias, path)
				if mode == "symlink" {
					if err := os.Symlink(alias, path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Link(alias, path); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
			case "missing":
				alias := path + ".moved"
				if err := os.Rename(path, alias); err != nil {
					t.Fatal(err)
				}
				defer os.Rename(alias, path)
			case "missing directory":
				t.Setenv("XDG_RUNTIME_DIR", privateTemp(t))
			}
			i := NewInspector()
			defer i.Close()
			if o := i.Inspect(e); o.Owner != OwnerPresent || o.Lock != LockUnknown {
				t.Fatal(o)
			}
		})
	}
	i := NewInspector()
	defer i.Close()
	for _, e := range []*Evidence{nil, {}, {LockKey: "../secret"}, {LockKey: strings.Repeat("x", 1000)}} {
		if o := i.Inspect(e); o.Owner != OwnerUnknown || o.Lock != LockUnknown {
			t.Fatal(o)
		}
	}
}

func TestInspectorCreatesNothing(t *testing.T) {
	runtime, state := privateTemp(t), privateTemp(t)
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", state)
	i := NewInspector()
	i.Close()
	for _, path := range []string{runtime, state} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			t.Fatalf("inspection created state: %v %v", entries, err)
		}
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(state, "absent"))
	i = NewInspector()
	i.Close()
	if _, err := os.Stat(filepath.Join(state, "absent")); !os.IsNotExist(err) {
		t.Fatalf("inspection created fallback: %v", err)
	}
}
