package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

type OperationRunner interface {
	Run(context.Context, borg.Request) (borg.Result, error)
}

type TaskResult struct {
	Result borg.Result
	Err    error
}

// Result includes only stages that were started, in configuration order.
type Result struct {
	Outcome borg.Outcome
	Stages  [][]TaskResult
}

type Executor struct {
	Runner OperationRunner
	Stdout io.Writer
	Stderr io.Writer
	// Output optionally shares attributed, atomic output chunks across workflow
	// runs and agent reports. When set it replaces Stdout and Stderr.
	Output *Output
	// AgentLock is owned by the agent for its entire lifetime. Passing it to
	// Borg prevents a replacement agent starting while orphan work survives.
	AgentLock *os.File
}

// RunLocked is the shared manual/scheduled entry point. ErrBusy means manual
// callers fail and scheduled callers skip; no caller should queue or retry it.
// Identity must be captured once when the configuration snapshot is loaded.
// Runner must be nil or a borg.Runner so descriptor inheritance is guaranteed.
func (e Executor) RunLocked(ctx context.Context, identity runlock.Identity, plan Plan) (result Result, runErr error) {
	events := lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name})
	delegated := false
	defer func() {
		if delegated {
			return
		}
		if errors.Is(runErr, runlock.ErrBusy) {
			skipRun(events, plan, lifecycle.Busy)
			return
		}
		events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
		reason := lifecycle.Unavailable
		if ctx.Err() != nil {
			reason = lifecycle.Cancelled
		}
		skipStages(events, plan, 0, reason)
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.FromBorg(result.Outcome)})
	}()
	failed := Result{Outcome: borg.Failure}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: borg.Interrupted}, err
	}
	store, err := runlock.Open()
	if err != nil {
		return failed, err
	}
	defer store.Close()
	lock, err := store.Workflow(identity, plan.Name)
	if err != nil {
		return failed, err
	}
	defer lock.Close()
	files := []*os.File{lock}
	if e.AgentLock != nil {
		files = append(files, e.AgentLock)
	}
	runner := borg.Runner{}
	if e.Runner != nil {
		var ok bool
		runner, ok = e.Runner.(borg.Runner)
		if !ok {
			return failed, errors.New("locked workflows require a Borg runner with descriptor inheritance")
		}
	}
	runner.InheritedFiles = append(append([]*os.File(nil), runner.InheritedFiles...), files...)
	e.Runner = runner
	delegated = true
	return e.run(ctx, plan, events)
}

// Run is the uncoordinated execution primitive; entry points use RunLocked.
// It waits for all siblings even after a warning or failure. Only cancellation
// interrupts active operations, and all runners return before Run returns.
func (e Executor) Run(ctx context.Context, plan Plan) (Result, error) {
	return e.run(ctx, plan, lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name}))
}

func (e Executor) run(ctx context.Context, plan Plan, events *lifecycle.Run) (result Result, runErr error) {
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	defer func() {
		reason := lifecycle.PriorFailure
		if ctx.Err() != nil {
			reason = lifecycle.Cancelled
		} else if result.Outcome == borg.Warning {
			reason = lifecycle.PriorWarning
		}
		skipStages(events, plan, len(result.Stages), reason)
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.FromBorg(result.Outcome)})
	}()
	runner := e.Runner
	if runner == nil {
		runner = borg.Runner{}
	}
	output := e.Output
	if output == nil {
		output = NewOutput(e.Stdout, e.Stderr)
	}
	result = Result{Outcome: borg.Success}
	for i, stage := range plan.Stages {
		if err := ctx.Err(); err != nil {
			result.Outcome = borg.Interrupted
			return result, err
		}
		results := make([]TaskResult, len(stage))
		events.Emit(lifecycle.Event{Kind: lifecycle.StageStarted, Stage: i + 1})
		var group sync.WaitGroup
		for j, task := range stage {
			group.Add(1)
			go func() {
				defer group.Done()
				event := lifecycle.Event{Stage: i + 1, Task: j + 1, Operation: task.Type, Repository: task.Repository}
				if ctx.Err() != nil {
					event.Kind, event.Outcome, event.Reason = lifecycle.TaskSkipped, lifecycle.Skipped, lifecycle.Cancelled
					events.Emit(event)
					results[j] = TaskResult{Result: borg.Result{Outcome: borg.Interrupted, ExitCode: -1}, Err: ctx.Err()}
					return
				}
				event.Kind = lifecycle.TaskStarted
				events.Emit(event)
				label := fmt.Sprintf("[workflow %q stage %d task %d %s repository %q] ",
					plan.Name, i+1, j+1, task.Type, task.Repository)
				results[j] = runTask(ctx, runner, output, label, task)
				event.Kind, event.Outcome = lifecycle.TaskFinished, lifecycle.FromBorg(results[j].Result.Outcome)
				if results[j].Err != nil && event.Outcome != lifecycle.Interrupted {
					event.Outcome = lifecycle.Failure
				}
				events.Emit(event)
			}()
		}
		group.Wait()
		result.Stages = append(result.Stages, results)
		var failures []error
		for j, task := range results {
			switch task.Result.Outcome {
			case borg.Success:
			case borg.Warning:
				if result.Outcome == borg.Success {
					result.Outcome = borg.Warning
				}
			default:
				// A child signal is a task failure, not a signal to Cairn.
				result.Outcome = borg.Failure
			}
			if task.Err != nil {
				result.Outcome = borg.Failure
				failures = append(failures, fmt.Errorf("stage %d task %d: %w", i+1, j+1, task.Err))
			}
		}
		if err := ctx.Err(); err != nil {
			result.Outcome = borg.Interrupted
			events.Emit(lifecycle.Event{Kind: lifecycle.StageFinished, Stage: i + 1, Outcome: lifecycle.Interrupted})
			return result, err
		}
		events.Emit(lifecycle.Event{Kind: lifecycle.StageFinished, Stage: i + 1, Outcome: lifecycle.FromBorg(result.Outcome)})
		if result.Outcome != borg.Success {
			return result, errors.Join(failures...)
		}
	}
	if err := ctx.Err(); err != nil {
		result.Outcome = borg.Interrupted
		return result, err
	}
	return result, nil
}

func skipStages(events *lifecycle.Run, plan Plan, from int, reason lifecycle.Reason) {
	for i := from; i < len(plan.Stages); i++ {
		events.Emit(lifecycle.Event{Kind: lifecycle.StageSkipped, Stage: i + 1, Outcome: lifecycle.Skipped, Reason: reason})
		for j, task := range plan.Stages[i] {
			events.Emit(lifecycle.Event{Kind: lifecycle.TaskSkipped, Stage: i + 1, Task: j + 1,
				Operation: task.Type, Repository: task.Repository, Outcome: lifecycle.Skipped, Reason: reason})
		}
	}
}

// Skip reports an unstarted occurrence using attribution-only plan fields.
func Skip(ctx context.Context, plan Plan, reason lifecycle.Reason) {
	skipRun(lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name}), plan, reason)
}

// Reject reports a failure before execution using attribution-only plan fields.
func Reject(ctx context.Context, plan Plan, reason lifecycle.Reason) {
	events := lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name})
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	skipStages(events, plan, 0, reason)
	events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Failure, Reason: reason})
}

func skipRun(events *lifecycle.Run, plan Plan, reason lifecycle.Reason) {
	skipStages(events, plan, 0, reason)
	events.Emit(lifecycle.Event{Kind: lifecycle.RunSkipped, Outcome: lifecycle.Skipped, Reason: reason})
}

func runTask(ctx context.Context, runner OperationRunner, output *Output, label string, task Task) TaskResult {
	failed := TaskResult{Result: borg.Result{Outcome: borg.Failure, ExitCode: -1}}
	if err := ctx.Err(); err != nil {
		failed.Result.Outcome, failed.Err = borg.Interrupted, err
		return failed
	}
	diagnostics := &taskWriter{output: output, label: label}
	if _, err := fmt.Fprintln(diagnostics, task.Scope); err != nil {
		failed.Err = err
		return failed
	}
	request := task.Request
	request.Diagnostics = diagnostics
	result, err := runner.Run(ctx, request)
	outcome := "failed"
	switch result.Outcome {
	case borg.Success:
		outcome = "completed"
	case borg.Warning:
		outcome = "completed with warnings"
	case borg.Interrupted:
		outcome = fmt.Sprintf("interrupted (signal %d)", result.Signal)
	}
	writer := &taskWriter{output: output, label: label, stdout: true}
	if _, outputErr := fmt.Fprintln(writer, outcome); outputErr != nil {
		result.Outcome = borg.Failure
		err = errors.Join(err, outputErr)
	}
	return TaskResult{Result: result, Err: err}
}

// Output serializes attributed task chunks and unlabelled diagnostic reports.
// One mutex covers both streams, even if they refer to the same writer.
type Output struct {
	mu             sync.Mutex
	stdout, stderr io.Writer
	outErr, errErr error
}

func NewOutput(stdout, stderr io.Writer) *Output {
	return &Output{stdout: stdout, stderr: stderr}
}

// Write emits an unlabelled diagnostic chunk, e.g. a scheduler lifecycle report.
func (o *Output) Write(p []byte) (int, error) {
	return (&taskWriter{output: o}).Write(p)
}

type taskWriter struct {
	output *Output
	label  string
	stdout bool
}

func (w *taskWriter) Write(p []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	writer, sticky := o.stderr, &o.errErr
	if w.stdout {
		writer, sticky = o.stdout, &o.outErr
	}
	if *sticky != nil {
		return 0, *sticky
	}
	if writer == nil {
		return len(p), nil
	}
	if w.label != "" {
		n, err := io.WriteString(writer, w.label)
		if err == nil && n != len(w.label) {
			err = io.ErrShortWrite
		}
		if err != nil {
			*sticky = err
			return 0, err
		}
	}
	n, err := writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	*sticky = err
	return n, err
}
