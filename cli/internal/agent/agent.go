// Package agent dispatches scheduled workflows from one startup configuration
// snapshot. It does not reload configuration, retry work, or queue missed runs.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/metrics"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/schedule"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

var ErrNoSchedules = errors.New("configuration has no scheduled workflows")

// Agent owns the loaded configuration. Callers must not mutate it or call Run
// concurrently. Each Run starts a fresh, nonpersistent scheduling session.
type Agent struct {
	selected *config.Config
	entries  []entry
	clock    clock
	// Tests replace only the dispatch boundary; production always uses RunLocked.
	execute func(context.Context, workflow.Plan) (workflow.Result, error)
}

type entry struct {
	name     string
	schedule *schedule.Schedule
	next     time.Time
	recheck  time.Time
}

// New prepares schedules without repository, credential, or lock IO. The
// supplied configuration must have been fully validated by config.Load.
func New(selected *config.Config) (*Agent, error) {
	a := &Agent{selected: selected, clock: wallClock{}}
	for name, definition := range selected.Workflows {
		if definition.Cron == "" {
			continue
		}
		location, err := schedule.LoadLocation(definition.Timezone)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
		parsed, err := schedule.Parse(definition.Cron, location)
		if err != nil {
			return nil, fmt.Errorf("workflow %q: %w", name, err)
		}
		a.entries = append(a.entries, entry{name: name, schedule: parsed})
	}
	if len(a.entries) == 0 {
		return nil, ErrNoSchedules
	}
	sort.Slice(a.entries, func(i, j int) bool { return a.entries[i].name < a.entries[j].name })
	return a, nil
}

// Run holds the config-level lock until all active workflows and their
// cancellation cleanup finish. Only agent infrastructure/output errors end the
// session; individual workflow failures are reported and do not stop dispatch.
func (a *Agent) Run(ctx context.Context, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := runlock.Config(a.selected.Path)
	if err != nil {
		return err
	}
	ctx = lifecycle.WithConfigID(ctx, identity.Digest())
	store, err := runlock.Open()
	if err != nil {
		return err
	}
	defer store.Close()
	lock, err := store.Agent(identity)
	if err != nil {
		if errors.Is(err, runlock.ErrBusy) {
			return errors.New("agent is already running for this configuration")
		}
		return err
	}
	defer lock.Close()
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &streams{stdout: stdout, stderr: stderr, cancel: cancel}
	out, diagnostics := stream{output, false}, stream{output, true}
	shared := workflow.NewOutput(out, diagnostics)
	report := func(format string, args ...any) {
		fmt.Fprintf(shared, "%s agent: %s\n", a.clock.Now().UTC().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
	}
	if a.selected.Metrics != nil {
		names := make([]string, 0, len(a.selected.Workflows))
		for name := range a.selected.Workflows {
			names = append(names, name)
		}
		collector, err := metrics.New(identity.Digest(), names)
		if err != nil {
			report("metrics collector failed: %v", err)
		} else {
			workCtx = lifecycle.WithAdditionalObserver(workCtx, collector.Observe)
			stopMetrics := serveMetrics(a.selected.Metrics.Listen, collector, report)
			defer stopMetrics()
		}
	}
	execute := a.execute
	if execute == nil {
		executor := workflow.Executor{Output: shared, AgentLock: lock}
		execute = func(ctx context.Context, plan workflow.Plan) (workflow.Result, error) {
			return executor.RunLocked(ctx, identity, plan)
		}
	}
	start := a.clock.Now()
	for i := range a.entries {
		a.entries[i].advance(start)
	}
	report("started; %d scheduled workflow(s); restart to load configuration changes", len(a.entries))
	active := make(map[string]bool)
	var activeMu sync.Mutex
	finished := make(chan struct{}, 1)
	var workers sync.WaitGroup
	for workCtx.Err() == nil {
		now := a.clock.Now()
		for i := range a.entries {
			if workCtx.Err() != nil {
				break
			}
			item := &a.entries[i]
			expected := item.next
			due, missed := item.due(now)
			if missed {
				a.skippedRun(workCtx, item.name, expected, lifecycle.Expired)
				report("workflow %q skipped expired occurrence(s); %s", item.name, a.skippedTasks(item.name))
			}
			if due.IsZero() {
				continue
			}
			name := item.name
			activeMu.Lock()
			busy := active[name]
			if !busy {
				active[name] = true
			}
			activeMu.Unlock()
			if busy {
				a.skippedRun(workCtx, name, due, lifecycle.Busy)
				report("workflow %q skipped: already running; %s", name, a.skippedTasks(name))
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer func() {
					activeMu.Lock()
					delete(active, name)
					activeMu.Unlock()
					// Completion only wakes the loop; coalesce notifications,
					// never queue results or block cleanup during shutdown.
					select {
					case finished <- struct{}{}:
					default:
					}
				}()
				if workCtx.Err() != nil {
					a.skippedRun(workCtx, name, due, lifecycle.Cancelled)
					return
				}
				dispatched := a.clock.Now()
				// A delayed goroutine is not permission to queue an expired run.
				if dispatched.Before(due) || !dispatched.Before(due.Add(time.Minute)) {
					a.skippedRun(workCtx, name, due, lifecycle.Expired)
					report("workflow %q skipped: occurrence is no longer current; %s", name, a.skippedTasks(name))
					return
				}
				plan, prepareErr := workflow.Prepare(a.selected, name, dispatched)
				if prepareErr != nil {
					workflow.Reject(lifecycle.WithSchedule(workCtx, due), a.attribution(name), lifecycle.PreparationFailed)
					report("workflow %q failed; %s: %v", name, a.skippedTasks(name), prepareErr)
					return
				}
				report("workflow %q dispatching (scheduled %s)", name, due.UTC().Format(time.RFC3339))
				if workCtx.Err() != nil {
					a.skippedRun(workCtx, name, due, lifecycle.Cancelled)
					return
				}
				// Output can block or the wall clock can jump during preparation.
				if now := a.clock.Now(); now.Before(due) || !now.Before(due.Add(time.Minute)) {
					a.skippedRun(workCtx, name, due, lifecycle.Expired)
					report("workflow %q skipped: occurrence is no longer current; %s", name, a.skippedTasks(name))
					return
				}
				result, runErr := execute(lifecycle.WithSchedule(workCtx, due), plan)
				if errors.Is(runErr, runlock.ErrBusy) {
					report("workflow %q skipped: already running; %s", name, result.Tasks)
					return
				}
				outcome := "failed"
				switch result.Outcome {
				case borg.Success:
					if runErr == nil {
						outcome = "completed"
					}
				case borg.Warning:
					outcome = "completed with warnings"
				case borg.Interrupted:
					outcome = "interrupted"
				}
				remaining := len(plan.Stages) - len(result.Stages)
				skipped := ""
				if remaining > 0 {
					skipped = fmt.Sprintf("; %d stage(s) not started", remaining)
				}
				if runErr != nil {
					report("workflow %q %s%s; %s: %v", name, outcome, skipped, result.Tasks, runErr)
				} else {
					report("workflow %q %s%s; %s", name, outcome, skipped, result.Tasks)
				}
			}()
		}
		// Short bounded waits observe wall-clock jumps even though Go timers
		// themselves use monotonic time. Never sleep until a far-future cron.
		timer := a.clock.NewTimer(time.Second)
		select {
		case <-workCtx.Done():
		case <-finished:
		case <-timer.C():
		}
		timer.Stop()
	}
	report("stopping; cancelling active workflows")
	cancel()
	workers.Wait()
	report("stopped")
	if err := output.err(); err != nil {
		return fmt.Errorf("agent output: %w", err)
	}
	return ctx.Err()
}

func (a *Agent) skippedRun(ctx context.Context, name string, due time.Time, reason lifecycle.Reason) {
	workflow.Skip(lifecycle.WithSchedule(ctx, due), a.attribution(name), reason)
}

func (a *Agent) skippedTasks(name string) workflow.TaskCounts {
	counts := workflow.TaskCounts{}
	for _, stage := range a.selected.Workflows[name].Stages {
		counts.Skipped += len(stage.Tasks)
	}
	return counts
}

func (a *Agent) attribution(name string) workflow.Plan {
	plan := workflow.Plan{Name: name}
	for _, stage := range a.selected.Workflows[name].Stages {
		tasks := make([]workflow.Task, len(stage.Tasks))
		for i, task := range stage.Tasks {
			tasks[i] = workflow.Task{Type: task.Type, Repository: task.Repository}
		}
		plan.Stages = append(plan.Stages, tasks)
	}
	return plan
}

func (e *entry) advance(after time.Time) {
	e.next = e.schedule.Next(after)
	// A zero Next is a bounded search result, not an invalid schedule. Revisit
	// it daily so a future occurrence can eventually enter the search horizon.
	e.recheck = after.Add(24 * time.Hour)
}

func (e *entry) due(now time.Time) (time.Time, bool) {
	if e.next.IsZero() {
		if now.Before(e.recheck) {
			return time.Time{}, false
		}
		// A large forward jump may land inside a newly feasible occurrence.
		e.next = e.schedule.Next(now.Truncate(time.Minute).Add(-time.Nanosecond))
		e.recheck = now.Add(24 * time.Hour)
		if e.next.IsZero() {
			return time.Time{}, false
		}
	}
	if now.Before(e.next) {
		return time.Time{}, false
	}
	due := e.next
	missed := !now.Before(due.Add(time.Minute))
	if missed {
		// Jump directly to this minute, not through a catch-up backlog.
		due = e.schedule.Next(now.Truncate(time.Minute).Add(-time.Nanosecond))
	}
	// Consume the occurrence before launching work, including failures/skips.
	// Keeping this future cursor on backward jumps prevents real-instant replay.
	e.advance(now)
	if due.IsZero() || due.After(now) {
		return time.Time{}, missed
	}
	return due, missed
}
