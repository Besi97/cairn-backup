package cli

import (
	"errors"
	"fmt"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/display"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/spf13/cobra"
)

// runOperation keeps manual operations on the same output and exit-code contract.
// previewResult is appended only after a completed preview, never after failure.
func runOperation(cmd *cobra.Command, request borg.Request, operation, status, previewResult string) error {
	preview, _ := cmd.Flags().GetBool("dry-run")
	output := display.NewOutput(cmd.OutOrStdout(), cmd.ErrOrStderr())
	display.AttachDiagnostics(cmd.Context(), output)
	var runID string
	ctx := lifecycle.WithAdditionalObserver(cmd.Context(), func(event lifecycle.Event) error {
		if event.Kind == lifecycle.RunStarted {
			runID = event.RunID
		}
		return output.Observe(event)
	})
	events := lifecycle.New(ctx, lifecycle.Event{
		Operation: cmd.Name(), Preview: preview,
	})
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	events.Emit(lifecycle.Event{Kind: lifecycle.TaskStarted, Task: 1})
	outcome := lifecycle.Failure
	defer func() {
		events.Emit(lifecycle.Event{Kind: lifecycle.TaskFinished, Task: 1, Outcome: outcome})
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: outcome})
	}()
	if _, err := fmt.Fprintln(output.Writer(false, ""), status); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	diagnostics := output.Writer(false, display.Label("borg", cmd.Name()))
	request.Diagnostics = diagnostics
	request.Progress = func(progress borg.Progress) error {
		return output.Progress(display.TaskID{Run: runID, Task: 1}, cmd.Name(), progress)
	}
	result, runErr := (borg.Runner{}).Run(cmd.Context(), request)
	flushErr := diagnostics.Flush()
	output.FinishTask(display.TaskID{Run: runID, Task: 1})
	if err := errors.Join(flushErr, output.Err()); err != nil {
		if result.Outcome == borg.Interrupted && borg.IsCancellation(runErr) {
			runErr = errors.Join(runErr, &borg.SecondaryError{Err: err})
		} else {
			result.Outcome = borg.Failure
			runErr = errors.Join(runErr, err)
		}
	}
	recordBorgResult(cmd.Context(), result, runErr)
	outcome = lifecycle.FromBorg(result.Outcome)
	code, renderedOutcome := exitFailure, "failed"
	switch result.Outcome {
	case borg.Success:
		code, renderedOutcome = exitSuccess, "completed"
	case borg.Warning:
		code, renderedOutcome = exitWarning, "completed with warnings"
	case borg.Interrupted:
		code, renderedOutcome = 128+result.Signal, "interrupted"
	}
	suffix := "."
	if previewResult != "" && (result.Outcome == borg.Success || result.Outcome == borg.Warning) {
		suffix = "; " + previewResult + "."
	}
	if _, err := fmt.Fprintf(output.Writer(true, ""), "%s %s%s\n", operation, renderedOutcome, suffix); err != nil {
		if result.Outcome == borg.Interrupted && borg.IsCancellation(runErr) {
			return &exitError{code: code, err: errors.Join(runErr, &borg.SecondaryError{Err: err})}
		}
		outcome = lifecycle.Failure
		return &exitError{code: exitFailure, err: err}
	}
	if code != exitSuccess {
		return &exitError{code: code, err: runErr}
	}
	return nil
}
