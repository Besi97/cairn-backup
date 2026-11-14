// Package cli handles Cairn's command-line interface and operation outcomes.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"

	"github.com/spf13/cobra"
)

const (
	exitSuccess      = 0
	exitWarning      = 1
	exitFailure      = 2
	exitInvalidInput = 3
)

// exitError distinguishes an executed operation from invalid command inputs.
// Warnings need an exit code even though the runner returns no error.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("operation completed with exit code %d", e.code)
}

func (e *exitError) Unwrap() error { return e.err }

// Run executes one invocation with isolated arguments and output streams.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	cmd := newRootCommand(version)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "Interrupted: %v\n", context.Cause(ctx))
		return 128 + int(cancellation.From(ctx))
	}
	err := cmd.ExecuteContext(ctx)
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "Interrupted: %v\n", context.Cause(ctx))
		return 128 + int(cancellation.From(ctx))
	}
	if err != nil {
		var operation *exitError
		if errors.As(err, &operation) {
			if operation.err != nil {
				fmt.Fprintf(stderr, "Error: %v\n", operation.err)
			}
			return operation.code
		}
		fmt.Fprintf(stderr, "Error: %v\nRun 'cairn --help' for usage.\n", err)
		return exitInvalidInput
	}
	return exitSuccess
}

func newRootCommand(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cairn",
		Short:         "Back up and maintain Borg repositories",
		Long:          "Cairn provides manual backups, repository maintenance, and scheduled workflows for Borg.",
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.SetVersionTemplate("cairn {{.Version}}\n")
	cmd.AddCommand(newBackupCommand(), newPruneCommand(), newCompactCommand())
	return cmd
}
