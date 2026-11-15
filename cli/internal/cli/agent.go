package cli

import (
	"github.com/Besi97/cairn-backup/cli/internal/agent"
	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "Run scheduled workflows"}
	var path string
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the foreground workflow scheduler",
		Long: "Load configuration once and run scheduled workflows until interrupted.\n" +
			"Restart to apply configuration changes. Missed runs are not caught up or retried.\n" +
			"Shutdown cancels active workflows and waits for subprocess cleanup.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := loadWorkflowConfig(cmd, path)
			if err != nil {
				return err
			}
			scheduler, err := agent.New(selected)
			if err != nil {
				return err
			}
			if err := scheduler.Run(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			return nil
		},
	}
	run.Flags().StringVar(&path, "config", "", "Configuration file (otherwise discover user then system configuration)")
	registerFileCompletion(run, "config")
	cmd.AddCommand(run)
	return cmd
}
