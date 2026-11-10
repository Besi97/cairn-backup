// Package backup creates Borg archives for configured jobs.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// Status is the outcome of one backup run.
type Status int

const (
	// Succeeded means Borg created the archive without warnings.
	Succeeded Status = iota
	// Warned means Borg created the archive but reported warnings, such as
	// files that changed while they were read.
	Warned
	// Failed means no complete archive was created.
	Failed
	// Interrupted means the run was cancelled before Borg finished.
	Interrupted
)

// Run creates one archive for the named job with Borg. Borg exit status 0 is
// Succeeded, 1 is Warned, and any other status is Failed. When ctx is cancelled,
// Run sends SIGTERM to Borg, waits for it to exit, and returns Interrupted.
// Borg's output goes to stdout and stderr. The passphrase is passed only in the
// environment, and Cairn never writes it to either stream.
func Run(ctx context.Context, cfg *config.Config, name string, stdout, stderr io.Writer) (Status, error) {
	if cfg == nil || cfg.Jobs[name] == nil {
		return Failed, fmt.Errorf("job %q is not configured", name)
	}
	job := cfg.Jobs[name]
	repository := cfg.Repositories[job.Repository]
	if repository == nil {
		return Failed, fmt.Errorf("repository %q is not configured", job.Repository)
	}
	lockWait, err := lockWaitSeconds(repository.LockWait)
	if err != nil {
		return Failed, fmt.Errorf("job %q: %w", name, err)
	}

	installation, err := borg.Detect(ctx)
	if err != nil {
		return failure(ctx, err)
	}
	if installation.Warning != "" {
		fmt.Fprintf(stderr, "cairn: warning: %s\n", installation.Warning)
	}

	passphrase, err := cfg.ReadPassphrase(job.Repository)
	if err != nil {
		return failure(ctx, err)
	}

	env := os.Environ()
	if passphrase != "" {
		env = append(env, "BORG_PASSPHRASE="+passphrase)
	}
	if repository.IdentityFile != "" {
		env = append(env, "BORG_RSH=ssh -i "+shellQuote(repository.IdentityFile))
	}

	archive := repository.Location + "::" + job.ArchivePrefix + "-{now:%Y-%m-%dT%H:%M:%S}"
	args := []string{"create", "--lock-wait", strconv.Itoa(lockWait), "--stats", "--", archive}
	args = append(args, job.Sources...)

	command := exec.Command(installation.Path, args...)
	command.Dir = job.SourceRoot
	command.Env = env
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return failure(ctx, fmt.Errorf("start borg: %w", err))
	}

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		// Ignore the error: Borg may have exited on its own just now.
		_ = command.Process.Signal(syscall.SIGTERM)
		waitErr = <-done
	}

	if waitErr == nil {
		return Succeeded, nil
	}
	if ctx.Err() != nil {
		return Interrupted, nil
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		switch exit.ExitCode() {
		case 0:
			return Succeeded, nil
		case 1:
			return Warned, nil
		default:
			return Failed, fmt.Errorf("borg create exited with status %d", exit.ExitCode())
		}
	}
	return Failed, fmt.Errorf("run borg create: %w", waitErr)
}

func failure(ctx context.Context, err error) (Status, error) {
	if ctx.Err() != nil {
		return Interrupted, nil
	}
	return Failed, err
}

// lockWaitSeconds converts a validated lock_wait value such as "60s" or "5m"
// to whole seconds for Borg's --lock-wait option.
func lockWaitSeconds(value string) (int, error) {
	number, unit := strings.TrimSuffix(value, "s"), 1
	if strings.HasSuffix(value, "m") {
		number, unit = strings.TrimSuffix(value, "m"), 60
	}
	seconds, err := strconv.Atoi(number)
	if err != nil || seconds <= 0 {
		return 0, errors.New("lock_wait is not a valid duration")
	}
	return seconds * unit, nil
}

// shellQuote quotes a path for Borg's BORG_RSH, which is split like a shell
// command line.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
