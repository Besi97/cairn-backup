package workflow

import (
	"context"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

// Manual is a prepared, sensitive configuration snapshot. Preparation does not
// start work; callers can inspect Plan and obtain confirmation before Run.
type Manual struct {
	Plan     Plan
	identity runlock.Identity
}

// IdentityError distinguishes lock identity failures from invalid workflow input.
type IdentityError struct{ Err error }

func (e *IdentityError) Error() string { return e.Err.Error() }
func (e *IdentityError) Unwrap() error { return e.Err }

// PrepareManual captures the plan and lock identity for a manual invocation.
func PrepareManual(selected *config.Config, name string, now time.Time) (Manual, error) {
	plan, err := Prepare(selected, name, now)
	if err != nil {
		return Manual{}, err
	}
	identity, err := runlock.Config(selected.Path)
	if err != nil {
		return Manual{}, &IdentityError{Err: err}
	}
	return Manual{Plan: plan, identity: identity}, nil
}

// Run waits for all work to stop, including on context cancellation. Lifecycle
// observers on ctx receive lifecycle events, including busy skips; executor
// output carries Borg progress. The CLI's context already records history.
func (m Manual) Run(ctx context.Context, executor Executor) (Result, error) {
	return executor.RunLocked(ctx, m.identity, m.Plan)
}
