package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/prune"
	"github.com/spf13/cobra"
)

func newPruneCommand() *cobra.Command {
	var options prune.Options
	var lockWait string
	var counts [7]string
	rules := []struct {
		name  string
		value *int
	}{
		{"secondly", &options.Retention.Secondly}, {"minutely", &options.Retention.Minutely},
		{"hourly", &options.Retention.Hourly}, {"daily", &options.Retention.Daily},
		{"weekly", &options.Retention.Weekly}, {"monthly", &options.Retention.Monthly},
		{"yearly", &options.Retention.Yearly},
	}
	cmd := &cobra.Command{
		Use:   "prune --repository LOCATION --archive-prefix PREFIX [RETENTION OPTIONS]",
		Short: "Prune selected archives in an existing Borg repository",
		Long: "Prune archives without configuration, a scheduler, or a preceding backup. Prune never compacts.\n\n" +
			"Require an explicit archive prefix and at least one effective retention rule.\n" +
			"The selector is PREFIX-* using Borg's placeholders and glob semantics unchanged.\n" +
			"It includes exact-name archives within that scope, not just timestamped backups.\n" +
			"Broader patterns can delete more archives; there is no confirmation prompt.\n\n" +
			"Borg applies retention to archive metadata timestamps in its local timezone.\n" +
			"Negative counts mean unlimited retention; zero disables a rule.\n" +
			"A dry-run lists archives Borg would keep or prune without deleting them.\n" +
			"It requires repository access and may use locks and local state.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, flag := range []string{"repository", "archive-prefix", "keep-within", "passphrase-file", "identity-file"} {
				value, _ := cmd.Flags().GetString(flag)
				if cmd.Flags().Changed(flag) && value == "" {
					return fmt.Errorf("--%s must not be empty", flag)
				}
			}
			for i, rule := range rules {
				count, err := strconv.Atoi(counts[i])
				if err != nil {
					return fmt.Errorf("--keep-%s must be a whole number; negative counts mean unlimited retention", rule.name)
				}
				*rule.value = count
			}
			if cmd.Flags().Changed("lock-wait") {
				seconds, err := strconv.Atoi(lockWait)
				if err != nil {
					return fmt.Errorf("--lock-wait must be a nonnegative whole number of seconds")
				}
				options.LockWait = &seconds
			}
			dir, err := os.Getwd()
			if err != nil {
				return &exitError{code: exitFailure, err: fmt.Errorf("get invocation directory: %w", err)}
			}
			prepared, err := prune.Prepare(options, dir)
			if err != nil {
				return err
			}
			operation, previewResult := "Prune", ""
			if options.DryRun {
				operation, previewResult = "Prune preview", "no archives deleted"
			}
			return runOperation(cmd, prepared.Request, operation,
				fmt.Sprintf("%s archive selector: %s", operation, prepared.Selector), previewResult)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.ArchivePrefix, "archive-prefix", "", "Select PREFIX-* using Borg's glob semantics (required)")
	flags.StringVar(&options.Retention.Within, "keep-within", "", "Keep all archives within INTERVAL: positive integer plus H, d, w, m, or y")
	for i, rule := range rules {
		flags.StringVar(&counts[i], "keep-"+rule.name, "0", "Number of "+rule.name+" archives to keep; negative means unlimited")
	}
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Read passphrase from this invocation-relative file")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	flags.BoolVar(&options.DryRun, "dry-run", false, "List archives Borg would keep or prune without deleting them")
	return cmd
}
