package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/display"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/drill"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

type OperationRunner interface {
	Run(context.Context, borg.Request) (borg.Result, error)
}

type TaskResult struct {
	Result   borg.Result
	Err      error
	Recovery *report.Evidence
}

// Stages includes started stages in configuration order; Tasks also counts skips.
type Result struct {
	Outcome borg.Outcome
	Stages  [][]TaskResult
	Tasks   TaskCounts
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
	ctx = lifecycle.WithConfigID(ctx, identity.Digest())
	ctx, presentation := e.presentation(ctx)
	defer func() { result.Tasks = presentation.counts }()
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
	events = lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name, Execution: runlock.Capture(lock)})
	delegated = true
	return e.run(ctx, plan, events, presentation)
}

// Run is the uncoordinated execution primitive; entry points use RunLocked.
// It waits for all siblings even after a warning or failure. Only cancellation
// interrupts active operations, and all runners return before Run returns.
func (e Executor) Run(ctx context.Context, plan Plan) (result Result, runErr error) {
	ctx, presentation := e.presentation(ctx)
	defer func() { result.Tasks = presentation.counts }()
	return e.run(ctx, plan, lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name}), presentation)
}

func (e Executor) run(ctx context.Context, plan Plan, events *lifecycle.Run, presentation *presentation) (result Result, runErr error) {
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	defer func() {
		reason := lifecycle.PriorFailure
		if ctx.Err() != nil {
			reason = lifecycle.Cancelled
		} else if result.Outcome == borg.Warning {
			reason = lifecycle.PriorWarning
		}
		skipStages(events, plan, len(result.Stages), reason)
		if outputErr := errors.Join(presentation.err, e.Output.Err()); outputErr != nil {
			result.Outcome = borg.Failure
			runErr = errors.Join(runErr, outputErr)
		}
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.FromBorg(result.Outcome)})
	}()
	runner := e.Runner
	if runner == nil {
		runner = borg.Runner{}
	}
	output := e.Output
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
				attribution := taskAttribution(plan.Name, i+1, j+1, task.Type, task.Repository)
				id := display.TaskID{Run: presentation.runID, Stage: i + 1, Task: j + 1}
				results[j] = runTask(ctx, runner, output, id, attribution, task)
				event.Kind, event.Outcome = lifecycle.TaskFinished, lifecycle.FromBorg(results[j].Result.Outcome)
				event.Recovery = results[j].Recovery
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
			return result, errors.Join(append([]error{err}, failures...)...)
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

func runTask(ctx context.Context, runner OperationRunner, output *Output, id display.TaskID, attribution string, task Task) TaskResult {
	failed := TaskResult{Result: borg.Result{Outcome: borg.Failure, ExitCode: -1}}
	if err := ctx.Err(); err != nil {
		failed.Result.Outcome, failed.Err = borg.Interrupted, err
		return failed
	}
	label := display.Label("cairn", attribution)
	if _, err := fmt.Fprintln(output.Writer(false, label), task.Scope); err != nil {
		failed.Err = err
		return failed
	}
	diagnostics := display.NewLines(output.Writer(false, display.Label("borg", attribution)))
	var result borg.Result
	var err error
	var recovery *report.Evidence
	if task.Command != nil {
		result, err = runCommand(ctx, runner, output, attribution, task.Command)
	} else if task.Drill != nil {
		borgRunner, ok := runner.(borg.Runner)
		if !ok {
			result = borg.Result{Outcome: borg.Failure, ExitCode: -1}
			err = errors.New("recovery drills require a Borg runner")
		} else {
			drilled, drillErr := drill.Run(ctx, borgRunner, *task.Drill, diagnostics)
			recovery = &drilled.Evidence
			result = borg.Result{Outcome: borg.Success, ExitCode: -1}
			switch {
			case ctx.Err() != nil:
				result.Outcome = borg.Interrupted
				err = ctx.Err()
			case drillErr != nil:
				result.Outcome = borg.Failure
				err = errors.New("recovery drill failed; inspect diagnostics and evidence")
			case drilled.Warning:
				result.Outcome = borg.Warning
			}
			if errors.Is(drillErr, borg.ErrProcessGroupNotStopped) {
				err = errors.Join(err, borg.ErrProcessGroupNotStopped)
			}
			err = errors.Join(err, report.WriteText(output.Writer(false, label), drilled.Evidence))
			if err != nil && result.Outcome != borg.Interrupted {
				result.Outcome = borg.Failure
			}
		}
	} else {
		request := task.Request
		request.Diagnostics = diagnostics
		request.Progress = func(progress borg.Progress) error { return output.Progress(id, attribution, progress) }
		result, err = runner.Run(ctx, request)
	}
	outputErr := diagnostics.Flush()
	output.FinishTask(id)
	if outputErr = errors.Join(outputErr, output.Err()); outputErr != nil {
		result.Outcome = borg.Failure
		err = errors.Join(err, outputErr)
	}
	outcome := "failed"
	switch result.Outcome {
	case borg.Success:
		outcome = "completed"
	case borg.Warning:
		outcome = "completed with warnings"
	case borg.Interrupted:
		outcome = "interrupted"
		if result.Signal != 0 {
			outcome = fmt.Sprintf("interrupted (signal %d)", result.Signal)
		}
	}
	writer := output.Writer(true, label)
	if _, outputErr := fmt.Fprintln(writer, outcome); outputErr != nil {
		result.Outcome = borg.Failure
		err = errors.Join(err, outputErr)
	}
	return TaskResult{Result: result, Err: err, Recovery: recovery}
}

func runCommand(ctx context.Context, runner OperationRunner, output *Output, attribution string, command *Command) (borg.Result, error) {
	result := borg.Result{Outcome: borg.Failure, ExitCode: -1}
	if command.Timeout <= 0 {
		return result, errors.New("invalid command timeout")
	}
	childCtx, cancel := context.WithTimeout(ctx, command.Timeout)
	defer cancel()
	cmd := exec.Command(command.Program, command.Args...)
	cmd.Dir = command.Dir
	if r, ok := runner.(borg.Runner); ok {
		cmd.ExtraFiles = r.InheritedFiles
	}
	stdout := output.Writer(true, display.Label("command stdout", attribution))
	stderr := output.Writer(false, display.Label("command stderr", attribution))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := borg.RunOwnedProcess(childCtx, cmd)
	outputErr := errors.Join(stdout.Flush(), stderr.Flush(), output.Err())
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = int(status.Signal())
		}
	}
	var cleanupErr error
	if errors.Is(err, borg.ErrProcessGroupNotStopped) {
		cleanupErr = fmt.Errorf("command process group did not stop: %w", borg.ErrProcessGroupNotStopped)
	}
	if ctx.Err() != nil {
		result.Outcome = borg.Interrupted
		return result, errors.Join(ctx.Err(), cleanupErr, outputErr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return result, errors.Join(errors.New("command timed out"), cleanupErr, outputErr)
	}
	if outputErr != nil {
		return result, errors.Join(errors.New("command output failed"), cleanupErr, outputErr)
	}
	if err != nil {
		if cleanupErr != nil {
			return result, cleanupErr
		}
		if result.Signal != 0 {
			return result, fmt.Errorf("command terminated by signal %d", result.Signal)
		}
		if result.ExitCode > 0 {
			return result, fmt.Errorf("command exited with code %d", result.ExitCode)
		}
		return result, errors.New("command could not run")
	}
	result.Outcome = borg.Success
	return result, nil
}

type Output = display.Output

func NewOutput(stdout, stderr io.Writer) *Output {
	return display.NewOutput(stdout, stderr)
}
