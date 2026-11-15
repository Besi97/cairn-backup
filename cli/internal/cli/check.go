package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/check"
	"github.com/spf13/cobra"
)

func newCheckCommand() *cobra.Command {
	var options check.Options
	var lockWait string
	cmd := &cobra.Command{
		Use:   "check --repository LOCATION [--verify-data]",
		Short: "Check an existing Borg repository and its archives without repair",
		Long: "Check an existing repository without configuration or a scheduler.\n\n" +
			"Checks repository consistency (including segment checksums) and all archive metadata.\n" +
			"By default, file contents are NOT cryptographically verified; use --verify-data\n" +
			"to also read, decrypt, and decompress all archived data. This can take a long time\n" +
			"and transfer substantial data for SSH repositories. Encrypted archives need their key and passphrase.\n" +
			"Checks never request repair, but may acquire locks and update Borg state.\n" +
			"Borg warnings can indicate corruption or skipped checks; inspect its diagnostics.\n" +
			"There are no archive selectors, partial checks, repair, or dry-run options.",
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
			request, err := check.Prepare(options, dir)
			if err != nil {
				return err
			}
			status := "Check scope: whole repository consistency and all archive metadata; file contents are NOT cryptographically verified (use --verify-data); no repair"
			if options.VerifyData {
				status = "Check scope: whole repository consistency, all archive metadata, and cryptographic file-content verification; may take a long time and transfer substantial remote data; no repair"
			}
			status += "\nBorg warnings can indicate corruption or skipped checks; inspect its diagnostics."
			return runOperation(cmd, request, "Check", status, "")
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.BoolVar(&options.VerifyData, "verify-data", false, "Also cryptographically verify archived file contents; reads all data and can be slow")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Passphrase file, relative to the invocation directory")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	registerRepositoryCompletion(cmd)
	registerFileCompletion(cmd, "passphrase-file")
	registerFileCompletion(cmd, "identity-file")
	registerNoFileCompletion(cmd, "lock-wait")
	return cmd
}
