package cli

import (
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/tui"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

func newTUICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the read-only terminal interface",
		Long: "Open the read-only terminal interface using the selected configuration.\n" +
			"Both standard input and standard output must be terminals.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runTUI(cmd)
		},
	}
	addTUIConfigFlag(cmd)
	return cmd
}

func addTUIConfigFlag(cmd *cobra.Command) {
	cmd.Flags().String("config", "", "Configuration file for the TUI (otherwise discover user then system configuration)")
	registerFileCompletion(cmd, "config")
}

func tuiTerminal(cmd *cobra.Command) bool {
	input, inOK := cmd.InOrStdin().(*os.File)
	output, outOK := cmd.OutOrStdout().(*os.File)
	return inOK && outOK && term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd())
}

func runDefaultTUI(cmd *cobra.Command) error {
	if !tuiTerminal(cmd) {
		return cmd.Help()
	}
	return runTUI(cmd)
}

func runTUI(cmd *cobra.Command) error {
	if !tuiTerminal(cmd) {
		return errors.New("cairn tui requires terminal standard input and output; run 'cairn --help' for CLI commands")
	}
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}
	selected, err := loadWorkflowConfig(cmd, path)
	if err != nil {
		return err
	}
	if err := tui.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), selected); err != nil {
		if errors.Is(err, tea.ErrInterrupted) {
			return &exitError{code: 130, err: err}
		}
		return &exitError{code: exitFailure, err: fmt.Errorf("terminal interface: %w", err)}
	}
	return nil
}
