package borg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// AccessOptions supplies repository access policy, independent of CLI flags.
// Credential paths are relative to the directory where Cairn was invoked.
// Request.Dir independently sets Borg's working directory for source paths.
// SSH must be set for an SSH repository. Local operations do not require ssh.
type AccessOptions struct {
	PassphraseFile string
	IdentityFile   string
	SSH            bool
}

type preparedAccess struct {
	env   []string
	files []*os.File
	rsh   string
}

func (a *preparedAccess) close() {
	for _, f := range a.files {
		_ = f.Close()
	}
}

func prepareAccess(options AccessOptions, env []string) (a preparedAccess, err error) {
	defer func() {
		if err != nil {
			a.close()
		}
	}()
	values := make(map[string]string)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	_, passphrase := values["BORG_PASSPHRASE"]
	_, command := values["BORG_PASSCOMMAND"]
	fd, descriptor := values["BORG_PASSPHRASE_FD"]
	// Shell-provided descriptors may not have close-on-exec set. Stop the
	// original from leaking to the probe or to unrelated child processes,
	// including when a higher-priority credential input overrides it.
	n, descriptorErr := protectDescriptor(fd)
	var file *os.File
	switch {
	case options.PassphraseFile != "":
		path, pathErr := filepath.Abs(options.PassphraseFile)
		if pathErr != nil {
			return a, fmt.Errorf("resolve passphrase file: %w", pathErr)
		}
		file, err = os.Open(path)
		if err != nil {
			return a, fmt.Errorf("open passphrase file: %w", err)
		}
		delete(values, "BORG_PASSPHRASE")
		delete(values, "BORG_PASSCOMMAND")
	case passphrase || command:
		// Borg selects these before a descriptor, even for an empty value.
		delete(values, "BORG_PASSPHRASE_FD")
	case descriptor:
		if descriptorErr != nil {
			return a, errors.New("BORG_PASSPHRASE_FD must identify an open readable descriptor")
		}
		// Duplicate rather than taking ownership of the caller's descriptor.
		// F_DUPFD_CLOEXEC avoids leaking it into concurrent subprocess launches.
		dup, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(n), syscall.F_DUPFD_CLOEXEC, 3)
		if errno != 0 {
			return a, errors.New("BORG_PASSPHRASE_FD must identify an open readable descriptor")
		}
		file = os.NewFile(dup, "Borg passphrase descriptor")
	default:
		// Prevent getpass from opening /dev/tty. Borg still decides whether
		// a passphrase is needed, and empty-passphrase keys remain usable.
		values["BORG_PASSPHRASE"] = ""
	}
	if file != nil {
		a.files = append(a.files, file)
		info, statErr := file.Stat()
		if statErr != nil || info.IsDir() {
			return a, errors.New("passphrase input must be a readable file or descriptor")
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), syscall.F_GETFL, 0)
		if errno != 0 || flags&syscall.O_ACCMODE == syscall.O_WRONLY {
			return a, errors.New("passphrase input must be a readable file or descriptor")
		}
		values["BORG_PASSPHRASE_FD"] = "3" // first os/exec ExtraFiles entry
	}
	if options.IdentityFile != "" && !options.SSH {
		return a, errors.New("identity file requires an SSH repository")
	}
	if options.SSH {
		a.rsh = "ssh -oBatchMode=yes -oStrictHostKeyChecking=yes"
		if options.IdentityFile != "" {
			path, pathErr := filepath.Abs(options.IdentityFile)
			if pathErr != nil {
				return a, fmt.Errorf("resolve identity file: %w", pathErr)
			}
			// Borg uses Python shlex.split, not a shell, for --rsh.
			a.rsh += " -i '" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
		}
		delete(values, "BORG_RSH")
	}
	a.env = make([]string, 0, len(values))
	for key, value := range values {
		a.env = append(a.env, key+"="+value)
	}
	return a, nil
}

func protectDescriptor(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 1<<31-1 {
		return 0, errors.New("invalid descriptor")
	}
	if n >= 3 {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(n), syscall.F_GETFD, 0)
		if errno != 0 {
			return 0, errno
		}
		_, _, errno = syscall.Syscall(syscall.SYS_FCNTL, uintptr(n), syscall.F_SETFD, flags|syscall.FD_CLOEXEC)
		if errno != 0 {
			return 0, errno
		}
	}
	return n, nil
}
