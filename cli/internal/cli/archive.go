package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/spf13/cobra"
)

func newArchiveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "List, inspect, restore, and mount archives",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newArchiveListingCommand(false), newArchiveListingCommand(true))
	cmd.AddCommand(newArchiveMountCommand(), newArchiveUnmountCommand())
	cmd.AddCommand(newRestoreCommand())
	return cmd
}

func newArchiveListingCommand(inspect bool) *cobra.Command {
	var options archive.Options
	var name, lockWait string
	var jsonOutput bool
	use, short := "list --repository LOCATION", "List all archives, including checkpoints"
	if inspect {
		use, short = "inspect --repository LOCATION --archive-name NAME", "List file metadata in one archive"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ".\n\n" +
			"Stream results to stdout and diagnostics to stderr without loading configuration.\n" +
			"Inspection reads metadata, not file contents, and does not verify data integrity.\n" +
			"Names and paths are display text, potentially lossy for non-text filenames;\n" +
			"do not use them as byte-exact restore selectors. Human output quotes text safely.\n" +
			"Output may be partial on failure or interruption; always check the exit code.",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, flag := range []string{"repository", "passphrase-file", "identity-file", "archive-name"} {
				if cmd.Flags().Lookup(flag) == nil {
					continue
				}
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
			var request borg.Request
			if inspect {
				request, err = archive.PrepareInspect(options, dir, name)
			} else {
				request, err = archive.PrepareList(options, dir)
			}
			if err != nil {
				return err
			}
			request.Diagnostics = cmd.ErrOrStderr()
			encoder := json.NewEncoder(cmd.OutOrStdout())
			var result borg.Result
			if inspect {
				result, err = archive.Inspect(cmd.Context(), borg.Runner{}, request, func(file archive.File) error {
					if jsonOutput {
						return encoder.Encode(file)
					}
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %8d %s %q", file.Mode, file.Size, file.Mtime, file.Path)
					if err == nil && file.LinkTarget != "" {
						_, err = fmt.Fprintf(cmd.OutOrStdout(), " -> %q", file.LinkTarget)
					}
					if err == nil {
						_, err = fmt.Fprintf(cmd.OutOrStdout(), " healthy=%t\n", file.Healthy)
					}
					return err
				})
			} else {
				result, err = archive.List(cmd.Context(), borg.Runner{}, request, func(entry archive.Archive) error {
					if jsonOutput {
						return encoder.Encode(entry)
					}
					_, err := fmt.Fprintf(cmd.OutOrStdout(), "%q %s [%s]\n", entry.Name, entry.Start, entry.ID)
					return err
				})
			}
			switch result.Outcome {
			case borg.Success:
				return nil
			case borg.Warning:
				return &exitError{code: exitWarning, err: err}
			case borg.Interrupted:
				return &exitError{code: 128 + result.Signal, err: err}
			default:
				return &exitError{code: exitFailure, err: err}
			}
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Passphrase file, relative to the invocation directory")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	flags.BoolVar(&jsonOutput, "json", false, "Stream Cairn JSON Lines records without a human summary")
	if inspect {
		flags.StringVar(&name, "archive-name", "", "Archive name; Borg expands placeholders (required)")
		registerNoFileCompletion(cmd, "archive-name")
	}
	registerRepositoryCompletion(cmd)
	registerFileCompletion(cmd, "passphrase-file")
	registerFileCompletion(cmd, "identity-file")
	registerNoFileCompletion(cmd, "lock-wait")
	return cmd
}
