// Package mount provides foreground Borg mounts and verified Linux unmounts.
package mount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

type Options struct {
	Repository     string
	ArchiveName    string
	PassphraseFile string
	IdentityFile   string
	LockWait       *int
}

func Prepare(options Options, invocationDir, mountpoint string) (borg.Request, error) {
	if options.ArchiveName == "" {
		return borg.Request{}, errors.New("--archive-name is required and must not be empty")
	}
	repository, err := borg.PrepareRepository(borg.RepositoryOptions{
		Repository: options.Repository, PassphraseFile: options.PassphraseFile, IdentityFile: options.IdentityFile,
	}, invocationDir)
	if err != nil {
		return borg.Request{}, err
	}
	lockArgs, err := borg.LockWaitArgs(options.LockWait)
	if err != nil {
		return borg.Request{}, err
	}
	target, err := Target(invocationDir, mountpoint)
	if err != nil {
		return borg.Request{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return borg.Request{}, fmt.Errorf("inspect mountpoint: %w", err)
	}
	if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return borg.Request{}, errors.New("mountpoint must be a user-owned directory, not group/world writable")
	}
	directory, err := os.Open(target)
	if err != nil {
		return borg.Request{}, fmt.Errorf("open mountpoint: %w", err)
	}
	defer directory.Close()
	entries, err := directory.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return borg.Request{}, fmt.Errorf("read mountpoint: %w", err)
	}
	if len(entries) != 0 {
		return borg.Request{}, errors.New("mountpoint must be empty")
	}
	args := append([]string{"mount", "--foreground"}, lockArgs...)
	args = append(args, "--", repository.Location+"::"+options.ArchiveName, target)
	return borg.Request{Args: args, Dir: invocationDir, Access: repository.Access}, nil
}

// Target resolves parents without accessing the final component, which may be a
// disconnected FUSE mount. A final symlink is not an exact mountpoint.
func Target(invocationDir, mountpoint string) (string, error) {
	if mountpoint == "" || !filepath.IsAbs(invocationDir) {
		return "", errors.New("mountpoint and absolute invocation directory are required")
	}
	if !filepath.IsAbs(mountpoint) {
		mountpoint = filepath.Join(invocationDir, mountpoint)
	}
	mountpoint = filepath.Clean(mountpoint)
	if mountpoint == "/" {
		return "", errors.New("the filesystem root is not an allowed mountpoint")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(mountpoint))
	if err != nil {
		return "", fmt.Errorf("resolve mountpoint parent: %w", err)
	}
	return filepath.Join(parent, filepath.Base(mountpoint)), nil
}

// Run requires a request returned by Prepare. It never detaches or attempts
// unmount cleanup after signals, process failures, or crashes.
func Run(ctx context.Context, runner borg.Runner, request borg.Request) (borg.Result, error) {
	failed := borg.Result{Outcome: borg.Failure, ExitCode: -1}
	if len(request.Args) == 0 {
		return failed, errors.New("missing prepared mount request")
	}
	target := request.Args[len(request.Args)-1]
	mounts, err := readMounts()
	if err != nil {
		return failed, err
	}
	if len(atTarget(mounts, target)) != 0 {
		return failed, errors.New("mountpoint already has a mounted filesystem")
	}
	fuse, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0)
	if err != nil {
		return failed, fmt.Errorf("FUSE unavailable: open /dev/fuse: %w; install/enable FUSE separately with your administrator, or use extraction instead", err)
	}
	if err := fuse.Close(); err != nil {
		return failed, fmt.Errorf("close FUSE dependency probe: %w", err)
	}
	return runAndVerifyGone(ctx, runner, request, target, readMounts)
}

// Unmount requires a Borg umount request with the exact target as its last
// argument. Verification does not establish Cairn ownership or prevent same-user
// replacement between reading mount metadata and executing the helper.
func Unmount(ctx context.Context, runner borg.Runner, request borg.Request) (borg.Result, error) {
	return unmount(ctx, runner, request, readMounts)
}

func unmount(ctx context.Context, runner borg.Runner, request borg.Request, load func() ([]entry, error)) (borg.Result, error) {
	failed := borg.Result{Outcome: borg.Failure, ExitCode: -1}
	if len(request.Args) == 0 {
		return failed, errors.New("missing prepared unmount request")
	}
	target := request.Args[len(request.Args)-1]
	mounts, err := load()
	if err != nil {
		return failed, err
	}
	if err := verify(mounts, target, os.Geteuid()); err != nil {
		return failed, err
	}
	return runAndVerifyGone(ctx, runner, request, target, load)
}

func runAndVerifyGone(ctx context.Context, runner borg.Runner, request borg.Request, target string, load func() ([]entry, error)) (borg.Result, error) {
	result, err := runner.Run(ctx, request)
	if err != nil {
		return result, err
	}
	mounts, err := load()
	if err == nil && len(atTarget(mounts, target)) != 0 {
		err = errors.New("mountpoint still has a mounted filesystem; no further cleanup attempted")
	}
	if err != nil {
		result.Outcome = borg.Failure
	}
	return result, err
}
