package borg

import (
	"errors"
	"strconv"
)

// DefaultLockWait is Cairn's native Borg lock-acquisition timeout in seconds.
const DefaultLockWait = 60

// LockWaitArgs prepares Borg's native lock timeout. Nil selects Cairn's default;
// a pointer to zero explicitly requests an immediate attempt. Command packages
// validate flag syntax before using this helper; the runner accepts prepared args.
func LockWaitArgs(seconds *int) ([]string, error) {
	wait := DefaultLockWait
	if seconds != nil {
		wait = *seconds
	}
	if wait < 0 {
		return nil, errors.New("lock wait must be a nonnegative whole number of seconds")
	}
	return []string{"--lock-wait", strconv.Itoa(wait)}, nil
}
