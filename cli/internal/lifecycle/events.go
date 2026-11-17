// Package lifecycle reports run activity without rendered output or Borg data.
package lifecycle

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

type Kind string
type Source string
type Outcome string
type Reason string

const (
	RunStarted    Kind = "run_started"
	RunFinished   Kind = "run_finished"
	RunSkipped    Kind = "run_skipped"
	StageStarted  Kind = "stage_started"
	StageFinished Kind = "stage_finished"
	StageSkipped  Kind = "stage_skipped"
	TaskStarted   Kind = "task_started"
	TaskFinished  Kind = "task_finished"
	TaskSkipped   Kind = "task_skipped"

	Manual    Source = "manual"
	Scheduled Source = "scheduled"

	Success     Outcome = "success"
	Warning     Outcome = "warning"
	Failure     Outcome = "failure"
	Interrupted Outcome = "interrupted"
	Skipped     Outcome = "skipped"

	Busy              Reason = "already_running"
	Expired           Reason = "expired_occurrence"
	PriorWarning      Reason = "prior_stage_warning"
	PriorFailure      Reason = "prior_stage_failure"
	Cancelled         Reason = "cancelled"
	PreparationFailed Reason = "preparation_failed"
	Unavailable       Reason = "execution_unavailable"
)

// Event contains only attribution and lifecycle state. Repository is a configured
// alias, never a location. Stage and Task are one-based; zero means not applicable.
// An expired-occurrence event may summarize multiple missed occurrences.
type Event struct {
	RunID       string
	Sequence    uint64
	Time        time.Time
	Source      Source
	ScheduledAt time.Time
	Workflow    string
	Operation   string
	Repository  string
	Preview     bool
	Kind        Kind
	Stage       int
	Task        int
	Outcome     Outcome
	Reason      Reason
}

type Observer func(Event) error

var ErrDelivery = errors.New("lifecycle event delivery failed")

type observerKey struct{}
type scheduleKey struct{}
type delivery struct {
	mu      sync.Mutex
	observe Observer
	parent  *delivery
	failed  atomic.Bool
}

// WithObserver serializes callbacks across all runs using the returned context.
// Callbacks must return promptly and must not recursively emit events. Errors
// never affect execution; Err reports a redacted delivery failure to the caller.
func WithObserver(ctx context.Context, observe Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, &delivery{observe: observe})
}

// WithAdditionalObserver retains existing observers, including their delivery
// failures. Every observer is called even when another observer fails.
func WithAdditionalObserver(ctx context.Context, observe Observer) context.Context {
	parent, _ := ctx.Value(observerKey{}).(*delivery)
	return context.WithValue(ctx, observerKey{}, &delivery{observe: observe, parent: parent})
}

func Err(ctx context.Context) error {
	d, _ := ctx.Value(observerKey{}).(*delivery)
	for ; d != nil; d = d.parent {
		if d.failed.Load() {
			return ErrDelivery
		}
	}
	return nil
}

func (d *delivery) emit(event Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	failed := false
	if d.parent != nil {
		failed = d.parent.emit(event)
	}
	if d.observe != nil && d.observe(event) != nil {
		failed = true
	}
	if failed {
		d.failed.Store(true)
	}
	return failed
}

func WithSchedule(ctx context.Context, due time.Time) context.Context {
	return context.WithValue(ctx, scheduleKey{}, due)
}

type Run struct {
	mu       sync.Mutex
	base     Event
	sequence uint64
	delivery *delivery
}

// New allocates one identity per attempted run, including skipped occurrences.
// Only attribution fields from base are retained. No event is emitted yet.
func New(ctx context.Context, base Event) *Run {
	d, _ := ctx.Value(observerKey{}).(*delivery)
	due, _ := ctx.Value(scheduleKey{}).(time.Time)
	source := Manual
	if !due.IsZero() {
		source = Scheduled
	}
	return &Run{delivery: d, base: Event{
		RunID: rand.Text(), Source: source, ScheduledAt: due,
		Workflow: base.Workflow, Operation: base.Operation,
		Repository: base.Repository, Preview: base.Preview,
	}}
}

func (r *Run) Emit(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	event.RunID, event.Sequence, event.Time = r.base.RunID, r.sequence, time.Now().UTC()
	event.Source, event.ScheduledAt = r.base.Source, r.base.ScheduledAt
	event.Workflow, event.Preview = r.base.Workflow, r.base.Preview
	if event.Operation == "" {
		event.Operation = r.base.Operation
	}
	if event.Repository == "" {
		event.Repository = r.base.Repository
	}
	if r.delivery != nil {
		r.delivery.emit(event)
	}
}

func FromBorg(outcome borg.Outcome) Outcome {
	switch outcome {
	case borg.Success:
		return Success
	case borg.Warning:
		return Warning
	case borg.Interrupted:
		return Interrupted
	default:
		return Failure
	}
}
