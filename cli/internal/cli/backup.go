package cli

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/backup"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/spf13/cobra"
)

func newBackupCommand() *cobra.Command {
	var options backup.Options
	var lockWait string
	cmd := &cobra.Command{
		Use:   "backup --repository LOCATION (--archive-prefix PREFIX | --archive-name NAME) [OPTIONS] PATH...",
		Short: "Create a backup in an existing Borg repository",
		Long: "Create a backup without configuration or a running scheduler. Backup never prunes or compacts.\n\n" +
			"Source paths, archive placeholders, and exclusion patterns use Borg's native semantics.\n" +
			"Use -- before source paths beginning with a hyphen.\n\n" +
			"A dry-run lists files without creating an archive. It does not verify repository access,\n" +
			"credentials, or that all file contents can be read.",
		RunE: func(cmd *cobra.Command, sources []string) error {
			if cmd.Flags().Changed("archive-prefix") == cmd.Flags().Changed("archive-name") {
				return fmt.Errorf("provide exactly one --archive-prefix or --archive-name")
			}
			for _, flag := range []string{"repository", "archive-prefix", "archive-name", "source-root", "passphrase-file", "identity-file"} {
				value, _ := cmd.Flags().GetString(flag)
				if cmd.Flags().Changed(flag) && value == "" {
					return fmt.Errorf("--%s must not be empty", flag)
				}
			}
			options.Sources = sources
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
			prepared, err := backup.Prepare(options, dir, time.Now())
			if err != nil {
				return err
			}
			operation := "Backup"
			previewResult := ""
			if options.DryRun {
				operation = "Backup preview"
				previewResult = "no archive created"
			}
			return runOperation(cmd, prepared.Request, operation,
				fmt.Sprintf("%s archive template: %s", operation, prepared.Archive), previewResult)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.ArchivePrefix, "archive-prefix", "", "Archive prefix; append a UTC timestamp with nanoseconds")
	flags.StringVar(&options.ArchiveName, "archive-name", "", "Exact archive name, without an added timestamp")
	flags.StringVar(&options.SourceRoot, "source-root", "", "Run Borg from this directory; relative sources resolve here")
	flags.StringArrayVar(&options.Excludes, "exclude", nil, "Exclude a Borg pattern (repeatable)")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Read passphrase from this invocation-relative file")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	flags.BoolVar(&options.DryRun, "dry-run", false, "List files without creating an archive; does not verify repository access")
	return cmd
}
