package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/compact"
	"github.com/spf13/cobra"
)

func newCompactCommand() *cobra.Command {
	var options compact.Options
	var lockWait string
	cmd := &cobra.Command{
		Use:   "compact --repository LOCATION",
		Short: "Reclaim eligible space across an existing Borg repository",
		Long: "Compact an existing repository without configuration, a scheduler, or a preceding backup or prune.\n\n" +
			"Compaction reclaims eligible space across the whole repository, not one archive prefix.\n" +
			"It does not delete archives and retains Borg's default compaction threshold.\n" +
			"No repository encryption key or passphrase is needed; SSH authentication still applies.\n" +
			"There is no dry-run or confirmation prompt.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, flag := range []string{"repository", "passphrase-file", "identity-file"} {
				value, _ := cmd.Flags().GetString(flag)
				if cmd.Flags().Changed(flag) && value == "" {
					return fmt.Errorf("--%s must not be empty", flag)
				}
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
			request, err := compact.Prepare(options, dir)
			if err != nil {
				return err
			}
			return runOperation(cmd, request, "Compact",
				"Compact scope: whole repository; reclaiming eligible space", "")
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Optional invocation-relative passphrase file; not needed for compaction")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	return cmd
}
