// Package cli handles Cairn's command-line interface without depending on Borg.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const (
	exitSuccess      = 0
	exitInvalidInput = 3
)

// Run executes one invocation with isolated arguments and output streams.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	cmd := newRootCommand(version)
	cmd.SetArgs(args)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if err := cmd.Execute(); err != nil {
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
	return cmd
}
