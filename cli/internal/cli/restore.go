package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/display"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/restore"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"
)

func newRestoreCommand() *cobra.Command {
	var options restore.Options
	var lockWait string
	var allowExisting bool
	cmd := &cobra.Command{
		Use:   "restore --repository LOCATION --archive-name NAME --destination DIR [PATH...]",
		Short: "Manually extract an archive or literal path prefixes to an explicit destination",
		Long: "Restore with native Borg metadata, subject to permissions and filesystem support.\n\n" +
			"No paths selects the whole archive; paths are literal archive-relative prefixes,\n" +
			"not patterns or the potentially lossy names printed by archive inspection.\n" +
			"Existing destinations require terminal confirmation or --allow-existing:\n" +
			"conflicting existing files may be replaced and directory metadata changed.\n" +
			"Symlinks in destination paths and archive traversal are refused regardless.\n" +
			"The repository and destination must be trusted and not modified concurrently.\n" +
			"This is not a sandbox or transaction; failures may leave partial replacements.\n" +
			"Preflight reads selected file data once before extraction. --dry-run performs\n" +
			"preflight and Borg's native dry-run without creating or changing the destination.\n" +
			"Preview needs no replacement confirmation, but Borg may update its own state.",
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, paths []string) error {
			for _, flag := range []string{"repository", "archive-name", "destination", "passphrase-file", "identity-file"} {
				value, _ := cmd.Flags().GetString(flag)
				if cmd.Flags().Changed(flag) && value == "" {
					return fmt.Errorf("--%s must not be empty", flag)
				}
			}
			if cmd.Flags().Changed("lock-wait") {
				seconds, err := strconv.Atoi(lockWait)
				if err != nil {
					return errors.New("--lock-wait must be a nonnegative whole number of seconds")
				}
				options.LockWait = &seconds
			}
			dir, err := os.Getwd()
			if err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			options.Paths = paths
			plan, err := restore.Prepare(options, dir)
			if err != nil {
				return err
			}
			existing, err := plan.Existing()
			if err != nil {
				return err
			}
			authorized := allowExisting
			if existing && !options.DryRun && !authorized {
				if !tuiTerminal(cmd) {
					return errors.New("existing destination: conflicting files may be replaced; use --allow-existing or terminal confirmation")
				}
				if err := confirmRestore(cmd, plan.Destination()); err != nil {
					return err
				}
				authorized = true
			}
			return runRestore(cmd, plan, authorized, options.DryRun)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.ArchiveName, "archive-name", "", "Literal archive name, without placeholders (required)")
	flags.StringVar(&options.Destination, "destination", "", "Destination directory with an existing parent (required)")
	flags.BoolVar(&allowExisting, "allow-existing", false, "Authorize replacement of conflicting files in an existing destination without prompting")
	flags.BoolVar(&options.DryRun, "dry-run", false, "Preflight and run Borg's extraction preview without changing the destination")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Passphrase file, relative to the invocation directory")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	registerRepositoryCompletion(cmd)
	registerFileCompletion(cmd, "passphrase-file")
	registerFileCompletion(cmd, "identity-file")
	registerDirectoryCompletion(cmd, "destination")
	registerNoFileCompletion(cmd, "archive-name", "lock-wait")
	return cmd
}

func confirmRestore(cmd *cobra.Command, destination string) error {
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Restore into %q? Conflicting existing files may be replaced and directory metadata changed. Partial changes cannot be rolled back. Type yes to continue: ", destination); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	// Read only a terminal, one byte at a time; never consume piped input or
	// leave a goroutine blocked on stdin after cancellation.
	input := cmd.InOrStdin().(*os.File)
	var answer strings.Builder
	for answer.Len() < 16 {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		ready, err := restoreInputReady(input, 100*time.Millisecond)
		if err != nil {
			return &exitError{code: exitFailure, err: err}
		}
		if !ready {
			continue
		}
		var b [1]byte
		if _, err := input.Read(b[:]); err != nil {
			return errors.New("restore confirmation was not received")
		}
		if b[0] == '\n' {
			if strings.TrimSpace(answer.String()) == "yes" {
				return nil
			}
			return errors.New("restore cancelled; destination unchanged")
		}
		answer.WriteByte(b[0])
	}
	return errors.New("restore confirmation must be exactly yes; destination unchanged")
}

func runRestore(cmd *cobra.Command, plan restore.Plan, authorized, preview bool) error {
	output := display.NewOutput(cmd.OutOrStdout(), cmd.ErrOrStderr())
	display.AttachDiagnostics(cmd.Context(), output)
	var runID string
	ctx := lifecycle.WithAdditionalObserver(cmd.Context(), func(event lifecycle.Event) error {
		if event.Kind == lifecycle.RunStarted {
			runID = event.RunID
		}
		return output.Observe(event)
	})
	events := lifecycle.New(ctx, lifecycle.Event{Operation: "restore", Preview: preview})
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	events.Emit(lifecycle.Event{Kind: lifecycle.TaskStarted, Task: 1})
	outcome := lifecycle.Failure
	defer func() {
		events.Emit(lifecycle.Event{Kind: lifecycle.TaskFinished, Task: 1, Outcome: outcome})
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: outcome})
	}()
	status := "Restore destination: %q; conflicting files may be replaced and directory metadata changed; no rollback.\n"
	if preview {
		status = "Restore preview destination: %q; destination will not be created or changed.\n"
	}
	if _, err := fmt.Fprintf(output.Writer(false, ""), status, plan.Destination()); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	if _, err := fmt.Fprintln(output.Writer(false, ""), "Repository and destination must be trusted and unchanged by other processes; not a sandbox. Preflight reads selected file data before native extraction."); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	id := display.TaskID{Run: runID, Task: 1}
	plan = plan.WithProgress(func(progress borg.Progress) error {
		return output.Progress(id, "restore", progress)
	})
	result, runErr := plan.Run(cmd.Context(), borg.Runner{}, authorized, output.Writer(false, display.Label("borg", "restore")))
	output.FinishTask(id)
	if err := output.Err(); err != nil {
		result.Outcome = borg.Failure
		runErr = errors.Join(runErr, err)
	}
	outcome = lifecycle.FromBorg(result.Outcome)
	code, text := exitFailure, "failed"
	switch result.Outcome {
	case borg.Success:
		code, text = exitSuccess, "completed"
	case borg.Warning:
		code, text = exitWarning, "completed with warnings"
	case borg.Interrupted:
		code, text = 128+result.Signal, "interrupted"
	}
	suffix := "; partial changes may remain."
	if preview {
		suffix = "; destination unchanged."
	} else if result.Outcome == borg.Success {
		suffix = "."
	}
	if _, err := fmt.Fprintf(output.Writer(true, ""), "Restore %s%s\n", text, suffix); err != nil {
		outcome = lifecycle.Failure
		return &exitError{code: exitFailure, err: err}
	}
	if code != exitSuccess {
		return &exitError{code: code, err: runErr}
	}
	return nil
}

func restoreInputReady(input *os.File, timeout time.Duration) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return n > 0, err
}
