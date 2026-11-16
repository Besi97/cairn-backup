package cli

import (
	"fmt"
	"os"
	"strconv"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/mount"
	"github.com/spf13/cobra"
)

func newArchiveMountCommand() *cobra.Command {
	var options mount.Options
	var lockWait string
	cmd := &cobra.Command{
		Use:   "mount --repository LOCATION --archive-name NAME MOUNTPOINT",
		Short: "Mount one archive read-only in the foreground",
		Long: "Mount one archive read-only, without loading configuration.\n\n" +
			"Requires separately installed FUSE support and an empty, user-owned directory\n" +
			"that is not group/world writable. Cairn stays running and Borg keeps its lock.\n" +
			"Close readers before unmounting from another terminal, or press Ctrl+C.\n" +
			"SIGTERM, crashes, and forced shutdown may leave a stale mount; no automatic\n" +
			"cleanup is attempted. Archived symlinks may point outside the mount.\n" +
			"No background mode, permission overrides, or arbitrary mount options.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range []string{"repository", "archive-name", "passphrase-file", "identity-file"} {
				value, _ := cmd.Flags().GetString(name)
				if cmd.Flags().Changed(name) && value == "" {
					return fmt.Errorf("--%s must not be empty", name)
				}
			}
			if cmd.Flags().Changed("lock-wait") {
				value, err := strconv.Atoi(lockWait)
				if err != nil {
					return fmt.Errorf("--lock-wait must be a nonnegative whole number of seconds")
				}
				options.LockWait = &value
			}
			dir, err := os.Getwd()
			if err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			request, err := mount.Prepare(options, dir, args[0])
			if err != nil {
				return err
			}
			return runMountOperation(cmd, request, false)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.Repository, "repository", "", "Existing local repository or Borg SSH location (required)")
	flags.StringVar(&options.ArchiveName, "archive-name", "", "Archive name; Borg expands placeholders (required)")
	flags.StringVar(&options.PassphraseFile, "passphrase-file", "", "Passphrase file, relative to the invocation directory")
	flags.StringVar(&options.IdentityFile, "identity-file", "", "SSH identity file, relative to the invocation directory")
	flags.StringVar(&lockWait, "lock-wait", strconv.Itoa(borg.DefaultLockWait), "Borg lock-acquisition timeout in `SECONDS`; zero does not wait")
	registerRepositoryCompletion(cmd)
	registerFileCompletion(cmd, "passphrase-file")
	registerFileCompletion(cmd, "identity-file")
	registerNoFileCompletion(cmd, "archive-name")
	registerNoFileCompletion(cmd, "lock-wait")
	return cmd
}

func newArchiveUnmountCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "unmount MOUNTPOINT",
		Short: "Unmount a verified same-user Borg FUSE mount",
		Long: "Unmount an exact Linux mountpoint verified as a same-user Borg FUSE mount.\n\n" +
			"Native Borg mounts are accepted; Cairn does not track mount ownership.\n" +
			"Missing, ambiguous, and unrelated mounts are refused. No force/lazy unmount,\n" +
			"privilege escalation, repository access, or configuration loading.\n" +
			"Close readers first. Concurrent same-user mount replacement is a trust boundary.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return &exitError{code: exitFailure, err: err}
			}
			target, err := mount.Target(dir, args[0])
			if err != nil {
				return err
			}
			request := borg.Request{Args: []string{"umount", "--", target}, Dir: dir}
			return runMountOperation(cmd, request, true)
		},
	}
}

func runMountOperation(cmd *cobra.Command, request borg.Request, unmount bool) error {
	events := lifecycle.New(cmd.Context(), lifecycle.Event{Operation: cmd.Name()})
	events.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	events.Emit(lifecycle.Event{Kind: lifecycle.TaskStarted, Task: 1})
	outcome := lifecycle.Failure
	defer func() {
		events.Emit(lifecycle.Event{Kind: lifecycle.TaskFinished, Task: 1, Outcome: outcome})
		events.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: outcome})
	}()
	status := "Starting foreground mount; close readers before Ctrl+C or 'cairn archive unmount'. SIGTERM/crashes may leave a stale mount."
	if unmount {
		status = "Verifying same-user Borg mount before unmounting."
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), status); err != nil {
		return &exitError{code: exitFailure, err: err}
	}
	request.Diagnostics = cmd.ErrOrStderr()
	run := mount.Run
	if unmount {
		run = mount.Unmount
	}
	result, err := run(cmd.Context(), borg.Runner{}, request)
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
	if _, writeErr := fmt.Fprintf(cmd.OutOrStdout(), "Archive %s %s.\n", cmd.Name(), text); writeErr != nil {
		outcome = lifecycle.Failure
		return &exitError{code: exitFailure, err: writeErr}
	}
	if code != exitSuccess {
		return &exitError{code: code, err: err}
	}
	return nil
}
