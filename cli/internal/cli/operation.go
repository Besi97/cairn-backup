package cli

import (
	"fmt"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/spf13/cobra"
)

// runOperation keeps manual operations on the same output and exit-code contract.
// previewResult is appended only after a completed preview, never after failure.
func runOperation(cmd *cobra.Command, request borg.Request, operation, status, previewResult string) error {
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), status); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	request.Diagnostics = cmd.ErrOrStderr()
	result, runErr := (borg.Runner{}).Run(cmd.Context(), request)
	code, outcome := exitFailure, "failed"
	switch result.Outcome {
	case borg.Success:
		code, outcome = exitSuccess, "completed"
	case borg.Warning:
		code, outcome = exitWarning, "completed with warnings"
	case borg.Interrupted:
		code, outcome = 128+result.Signal, "interrupted"
	}
	suffix := "."
	if previewResult != "" && (result.Outcome == borg.Success || result.Outcome == borg.Warning) {
		suffix = "; " + previewResult + "."
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s%s\n", operation, outcome, suffix); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	if code != exitSuccess {
		return &exitError{code: code, err: runErr}
	}
	return nil
}
