// Package cancellation preserves the signal that initiated context cancellation.
package cancellation

import (
	"context"
	"errors"
	"syscall"
)

// Signal is a cancellation cause, independent of the child's eventual status.
type Signal syscall.Signal

func (s Signal) Error() string { return syscall.Signal(s).String() }

// From returns the initiating signal, or SIGTERM for programmatic cancellation.
func From(ctx context.Context) syscall.Signal {
	var signal Signal
	if errors.As(context.Cause(ctx), &signal) {
		return syscall.Signal(signal)
	}
	return syscall.SIGTERM
}
