package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
	"github.com/spf13/cobra"
)

func newWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Short: "List and run configured workflows"}
	var listPath string
	list := &cobra.Command{
		Use:               "list",
		Short:             "List configured workflow names",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options, err := config.Environment()
			if err != nil {
				return err
			}
			options.Path, options.Explicit = listPath, cmd.Flags().Changed("config")
			selected, err := config.Load(options)
			if err != nil {
				return err
			}
			names := make([]string, 0, len(selected.Workflows))
			for name := range selected.Workflows {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
					return &exitError{code: exitFailure, err: err}
				}
			}
			return nil
		},
	}
	list.Flags().StringVar(&listPath, "config", "", "Configuration file (otherwise discover user then system configuration)")
	registerFileCompletion(list, "config")
	var runPath string
	run := &cobra.Command{
		Use:   "run NAME",
		Short: "Run a workflow now, without a scheduler",
		Long: "Run stages in order, with concurrent tasks in each stage. Warnings and failures\n" +
			"stop later stages after all siblings finish. Cancellation stops active tasks.\n" +
			"Standalone operation flags do not override configured tasks.\n" +
			"Cross-process workflow overlap protection is not implemented yet.",
		Args: cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
			if len(args) != 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			options, err := config.Environment()
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			options.Path, options.Explicit = runPath, cmd.Flags().Changed("config")
			selected, err := config.Load(options)
			if err != nil {
				// Completion is advisory; config check provides diagnostics.
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			names := make([]string, 0, len(selected.Workflows))
			for name := range selected.Workflows {
				if strings.HasPrefix(name, toComplete) {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			completions := make([]cobra.Completion, 0, len(names))
			for _, name := range names {
				completions = append(completions, name)
			}
			return completions, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			options, err := config.Environment()
			if err != nil {
				return err
			}
			options.Path, options.Explicit = runPath, cmd.Flags().Changed("config")
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
	run.Flags().StringVar(&runPath, "config", "", "Configuration file (otherwise discover user then system configuration)")
	registerFileCompletion(run, "config")
	cmd.AddCommand(list, run)
	return cmd
}
