// Package runlock coordinates local Cairn processes, independently of Borg's
// authoritative repository locks. Locks are nonblocking and never unlinked.
package runlock

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrBusy is contention, not a filesystem failure. Scheduled callers skip it.
var ErrBusy = errors.New("already running")

// Identity is captured once at startup, including for an agent config snapshot.
// Resolving identity must not change the selected config's relative-path base.
type Identity struct{ path string }

func Config(path string) (Identity, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve lock identity: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Identity{}, fmt.Errorf("resolve lock identity: %w", err)
	}
	return Identity{path: canonical}, nil
}

// Store holds an open private directory so later opens cannot follow a replaced
// pathname. All participants must use the same runtime/state environment.
type Store struct{ directory *os.File }

// Open selects $XDG_RUNTIME_DIR/cairn/locks if the runtime directory is private
// and owned by this user, otherwise a private per-user XDG state fallback.
// An unsafe existing Cairn directory is an error, not a reason to switch roots.
func Open() (*Store, error) {
	return open(os.Getenv("XDG_RUNTIME_DIR"), os.Getenv("XDG_STATE_HOME"), os.Getenv("HOME"))
}

func open(runtime, state, home string) (*Store, error) {
	var base *os.File
	if filepath.IsAbs(runtime) {
		base, _ = privateDirectory(runtime)
	}
	if base == nil {
		if !filepath.IsAbs(state) {
			if !filepath.IsAbs(home) {
				return nil, errors.New("cannot resolve private workflow lock directory")
			}
			state = filepath.Join(home, ".local", "state")
		}
		// The XDG state parent may be shared; only Cairn's directories must be
		// private. No lock files are opened through this pathname.
		if err := os.MkdirAll(state, 0700); err != nil {
			return nil, fmt.Errorf("create lock state directory: %w", err)
		}
		var err error
		base, err = os.Open(state)
		if err != nil {
			return nil, fmt.Errorf("open lock state directory: %w", err)
		}
		info, err := base.Stat()
		if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) ||
			info.Mode().Perm()&0022 != 0 {
			base.Close()
			return nil, errors.New("lock state directory must be user-owned and not group/world writable")
		}
	}
	defer base.Close()
	cairn, err := privateChild(base, "cairn")
	if err != nil {
		return nil, err
	}
	defer cairn.Close()
	directory, err := privateChild(cairn, "locks")
	if err != nil {
		return nil, err
	}
	return &Store{directory: directory}, nil
}

func privateDirectory(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return checkDirectory(os.NewFile(uintptr(fd), path))
}

func privateChild(parent *os.File, name string) (*os.File, error) {
	if err := syscall.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return nil, fmt.Errorf("create private lock directory: %w", err)
	}
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open private lock directory: %w", err)
	}
	return checkDirectory(os.NewFile(uintptr(fd), name))
}

func checkDirectory(file *os.File) (*os.File, error) {
	info, err := file.Stat()
	if err != nil || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) ||
		info.Sys().(*syscall.Stat_t).Mode&07777 != 0700 {
		file.Close()
		return nil, errors.New("lock directory must be owned by the current user with mode 0700")
	}
	return file, nil
}

func (s *Store) Close() error { return s.directory.Close() }

// Workflow and Agent occupy separate namespaces even for unusual workflow names.
func (s *Store) Workflow(identity Identity, name string) (*os.File, error) {
	return s.acquire(identity, "workflow", name)
}

func (s *Store) Agent(identity Identity) (*os.File, error) {
	return s.acquire(identity, "agent", "")
}

func (s *Store) acquire(identity Identity, kind, name string) (*os.File, error) {
	if identity.path == "" {
		return nil, errors.New("missing canonical configuration lock identity")
	}
	data, _ := json.Marshal([]string{kind, identity.path, name})
	filename := fmt.Sprintf("%s-%x.lock", kind, sha256.Sum256(data))
	fd, err := syscall.Openat(int(s.directory.Fd()), filename,
		syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("open %s lock: %w", kind, err)
	}
	file := os.NewFile(uintptr(fd), filename)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		stat.Uid != uint32(os.Geteuid()) || stat.Mode&07777 != 0600 || stat.Nlink != 1 {
		file.Close()
		return nil, fmt.Errorf("unsafe %s lock file", kind)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("acquire %s lock: %w", kind, err)
	}
	// Release ONLY by closing this descriptor. LOCK_UN would also release the
	// shared open-file-description lock retained by surviving Borg children.
	return file, nil
}
