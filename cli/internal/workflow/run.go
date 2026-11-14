package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
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
}

// Run waits for all siblings even after a warning or failure. Only cancellation
// interrupts active operations, and all runners return before Run returns.
func (e Executor) Run(ctx context.Context, plan Plan) (Result, error) {
	runner := e.Runner
	if runner == nil {
		runner = borg.Runner{}
	}
	output := &outputStreams{stdout: e.Stdout, stderr: e.Stderr}
	result := Result{Outcome: borg.Success}
	for i, stage := range plan.Stages {
		if err := ctx.Err(); err != nil {
			result.Outcome = borg.Interrupted
			return result, err
		}
		results := make([]TaskResult, len(stage))
		var group sync.WaitGroup
		for j, task := range stage {
			group.Add(1)
			go func() {
				defer group.Done()
				label := fmt.Sprintf("[workflow %q stage %d task %d %s repository %q] ",
					plan.Name, i+1, j+1, task.Type, task.Repository)
				results[j] = runTask(ctx, runner, output, label, task)
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
			return result, err
		}
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

func runTask(ctx context.Context, runner OperationRunner, output *outputStreams, label string, task Task) TaskResult {
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

// One mutex covers both streams, even if callers supply the same writer. Each
// chunk is attributed without buffering arbitrary Borg output or progress lines.
type outputStreams struct {
	mu             sync.Mutex
	stdout, stderr io.Writer
	outErr, errErr error
}

type taskWriter struct {
	output *outputStreams
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
	n, err := io.WriteString(writer, w.label)
	if err == nil && n != len(w.label) {
		err = io.ErrShortWrite
	}
	if err != nil {
		*sticky = err
		return 0, err
	}
	n, err = writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	*sticky = err
	return n, err
}
