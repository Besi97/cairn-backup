package borg

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ReusableAccess preserves access precedence for a multi-process operation.
// An inherited credential descriptor is consumed once into a sealed anonymous
// file, then reopened independently by each Runner.Run. Close after all children
// exit. Secret bytes never become arguments, environment values, or disk files.
func ReusableAccess(ctx context.Context, options AccessOptions) (AccessOptions, func(), error) {
	cleanup := func() {}
	access, err := prepareAccess(options, environment(nil))
	if err != nil {
		return options, cleanup, err
	}
	defer access.close()
	if options.Passphrase != nil || options.PassphraseFile != "" || len(access.files) == 0 {
		return options, cleanup, nil
	}
	fd, err := unix.MemfdCreate("cairn-borg-passphrase", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return options, cleanup, errors.New("create reusable credential descriptor")
	}
	file := os.NewFile(uintptr(fd), "reusable Borg credential")
	cleanup = func() { _ = file.Close() }
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	input := access.files[0]
	buffer := make([]byte, 4096)
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return options, cleanup, err
		}
		fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		if n, err := unix.Poll(fds, 100); errors.Is(err, unix.EINTR) || (err == nil && n == 0) {
			continue
		} else if err != nil {
			return options, cleanup, errors.New("read reusable credential descriptor")
		}
		n, err := unix.Read(int(input.Fd()), buffer)
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
			continue
		}
		if err != nil {
			return options, cleanup, errors.New("read reusable credential descriptor")
		}
		if n == 0 {
			break
		}
		total += n
		if total > 1<<20 {
			return options, cleanup, errors.New("credential descriptor exceeds 1 MiB")
		}
		if _, err := file.Write(buffer[:n]); err != nil {
			return options, cleanup, errors.New("snapshot reusable credential descriptor")
		}
	}
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		return options, cleanup, errors.New("seal reusable credential descriptor")
	}
	options.PassphraseFile = fmt.Sprintf("/proc/self/fd/%d", file.Fd())
	ok = true
	return options, cleanup, nil
}
