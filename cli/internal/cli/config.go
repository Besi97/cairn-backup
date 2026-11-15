package cli

import (
	"fmt"

	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "config", Short: "Check optional Cairn configuration",
	}
	var path string
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate configuration without Borg or repository access",
		Long: "Validate the entire selected configuration without executing Borg, opening credential files, or checking repository/source existence.\n" +
			"Checks cron syntax and timezone names offline; the agent is not implemented yet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options, err := config.Environment()
			if err != nil {
				return err
			}
			options.Path, options.Explicit = path, cmd.Flags().Changed("config")
			selected, err := config.Load(options)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Configuration valid: %s\n", selected.Path); err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			return nil
		},
	}
	check.Flags().StringVar(&path, "config", "", "Configuration file (otherwise discover user then system configuration)")
	registerFileCompletion(check, "config")
	check.ValidArgsFunction = cobra.NoFileCompletions
	cmd.AddCommand(check)
	return cmd
}
