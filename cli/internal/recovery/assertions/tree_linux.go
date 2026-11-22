package assertions

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

var (
	errInvalidPath        = errors.New("invalid assertion path")
	errParentNotDirectory = errors.New("path parent is not a directory")
)

func openRoot(path string) (*os.File, error) {
	if path == "" {
		return nil, fs.ErrInvalid
	}
	// Strip trailing separators so O_NOFOLLOW applies to the root itself.
	path = filepath.Clean(path)
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "assertion root"), nil
}

type node struct {
	file *os.File
	stat unix.Stat_t
}

// lookup pins the leaf without opening it for I/O. Each parent is opened
// relative to a pinned directory and must be a real directory.
func lookup(ctx context.Context, root *os.File, name string) (node, error) {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." ||
		strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
		return node{}, errInvalidPath
	}
	dir := root
	defer func() {
		if dir != root {
			_ = dir.Close()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return node{}, err
		}
		part, rest, parent := strings.Cut(name, "/")
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if parent {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(dir.Fd()), part, flags, 0)
		if parent && errors.Is(err, unix.ENOTDIR) {
			return node{}, errParentNotDirectory
		}
		if err != nil {
			return node{}, err
		}
		f := os.NewFile(uintptr(fd), "assertion node")
		if !parent {
			n := node{file: f}
			if err := unix.Fstat(fd, &n.stat); err != nil {
				_ = f.Close()
				return node{}, err
			}
			return n, nil
		}
		if dir != root {
			_ = dir.Close()
		}
		dir, name = f, rest
	}
}

func (n node) kind() fs.FileMode {
	switch n.stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return 0
	case unix.S_IFDIR:
		return fs.ModeDir
	case unix.S_IFLNK:
		return fs.ModeSymlink
	case unix.S_IFIFO:
		return fs.ModeNamedPipe
	case unix.S_IFSOCK:
		return fs.ModeSocket
	case unix.S_IFBLK:
		return fs.ModeDevice
	case unix.S_IFCHR:
		return fs.ModeDevice | fs.ModeCharDevice
	default:
		return fs.ModeIrregular
	}
}

func (n node) openRegular() (*os.File, error) {
	if n.kind() != 0 {
		return nil, errors.New("not a regular file")
	}
	// Reopen the pinned inode through trusted procfs, not the restored name.
	// A check-then-open by name could open a replaced FIFO or device.
	return os.Open("/proc/self/fd/" + strconv.FormatUint(uint64(n.file.Fd()), 10))
}

func countRegular(ctx context.Context, dir *os.File) (int64, error) {
	return countBelow(ctx, dir, ".")
}

func countBelow(ctx context.Context, parent *os.File, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	dir := os.NewFile(uintptr(fd), "assertion directory")
	defer dir.Close()

	var count int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		names, err := dir.Readdirnames(128)
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
		for _, child := range names {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(fd, child, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return 0, err
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				count++
			case unix.S_IFDIR:
				n, err := countBelow(ctx, dir, child)
				if err != nil {
					return 0, err
				}
				count += n
			}
		}
	}
}
