package cli

import (
	"fmt"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
	"github.com/spf13/cobra"
)

func newWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Short: "Run configured workflows"}
	var path string
	run := &cobra.Command{
		Use:   "run NAME",
		Short: "Run a workflow now, without a scheduler",
		Long: "Run stages in order, with concurrent tasks in each stage. Warnings and failures\n" +
			"stop later stages after all siblings finish. Cancellation stops active tasks.\n" +
			"Standalone operation flags do not override configured tasks.\n" +
			"Cross-process workflow overlap protection is not implemented yet.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options, err := config.Environment()
			if err != nil {
				return err
			}
			options.Path, options.Explicit = path, cmd.Flags().Changed("config")
			selected, err := config.Load(options)
			if err != nil {
				return err
			}
			plan, err := workflow.Prepare(selected, args[0], time.Now())
			if err != nil {
				return err
			}
			result, runErr := (workflow.Executor{
				Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			}).Run(cmd.Context(), plan)
			code, outcome := exitFailure, "failed"
			switch result.Outcome {
			case borg.Success:
				code, outcome = exitSuccess, "completed"
			case borg.Warning:
				code, outcome = exitWarning, "completed with warnings"
			case borg.Interrupted:
				outcome = "interrupted"
			}
			skipped := ""
			if remaining := len(plan.Stages) - len(result.Stages); remaining > 0 {
				skipped = fmt.Sprintf("; %d later stage(s) not started", remaining)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Workflow %q %s%s.\n", plan.Name, outcome, skipped); err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			if code != exitSuccess {
				return &exitError{code: code, err: runErr}
			}
			return nil
		},
	}
	run.Flags().StringVar(&path, "config", "", "Configuration file (otherwise discover user then system configuration)")
	cmd.AddCommand(run)
	return cmd
}
