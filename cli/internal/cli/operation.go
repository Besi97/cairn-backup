package cli

import (
	"fmt"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/spf13/cobra"
)

// runOperation keeps manual operations on the same output and exit-code contract.
// previewResult is appended only after a completed preview, never after failure.
func runOperation(cmd *cobra.Command, request borg.Request, operation, status, previewResult string) error {
	preview, _ := cmd.Flags().GetBool("dry-run")
	events := lifecycle.New(cmd.Context(), lifecycle.Event{
		Operation: cmd.Name(), Preview: preview,
	})
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	events.Emit(lifecycle.Event{Kind: lifecycle.TaskStarted, Task: 1})
	outcome := lifecycle.Failure
	defer func() {
		events.Emit(lifecycle.Event{Kind: lifecycle.TaskFinished, Task: 1, Outcome: outcome})
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: outcome})
	}()
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), status); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	request.Diagnostics = cmd.ErrOrStderr()
	result, runErr := (borg.Runner{}).Run(cmd.Context(), request)
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
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s%s\n", operation, renderedOutcome, suffix); err != nil {
		outcome = lifecycle.Failure
		return &exitError{code: exitFailure, err: err}
	}
	if code != exitSuccess {
		return &exitError{code: code, err: runErr}
	}
	return nil
}
