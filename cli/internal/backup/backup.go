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
	// Borg reads a bare "-" as stdin even after "--", so every source is
	// prefixed. Archive paths are unchanged by the prefix.
	for _, source := range job.Sources {
		args = append(args, "./"+source)
	}

	status, err := runBorg(ctx, installation.Path, env, job.SourceRoot, args, stdout, stderr)
	if err != nil || status == Interrupted {
		return status, err
	}
	if job.Maintenance == nil || job.Maintenance.Retention == nil {
		return status, nil
	}
	if status == Warned && job.Maintenance.AfterBackupWarning != "run" {
		return Warned, nil
	}
	if ctx.Err() != nil {
		return Interrupted, nil
	}

	pruneStatus, err := runBorg(ctx, installation.Path, env, job.SourceRoot, pruneArgs(repository.Location, lockWait, job), stdout, stderr)
	if err != nil || pruneStatus == Interrupted {
		return pruneStatus, err
	}
	if status == Warned || pruneStatus == Warned {
		return Warned, nil
	}
	return Succeeded, nil
}

// runBorg runs one Borg command, forwards its output, and classifies its exit.
// When ctx is cancelled, it sends SIGTERM and waits for Borg to exit.
func runBorg(ctx context.Context, path string, env []string, dir string, args []string, stdout, stderr io.Writer) (Status, error) {
	command := exec.Command(path, args...)
	command.Dir = dir
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
		return classifyExit(args[0], exit.ExitCode())
	}
	return Failed, fmt.Errorf("run borg %s: %w", args[0], waitErr)
}

// pruneArgs builds a prune command limited to this job's archives. Only the
// retention rules that are configured are passed to Borg.
func pruneArgs(location string, lockWait int, job *config.Job) []string {
	retention := job.Maintenance.Retention
	args := []string{"prune", "--lock-wait", strconv.Itoa(lockWait), "--glob-archives", archiveGlob(job.ArchivePrefix)}
	if retention.KeepWithin != "" {
		args = append(args, "--keep-within", retention.KeepWithin)
	}
	for _, rule := range []struct {
		flag  string
		value *int
	}{
		{"--keep-hourly", retention.KeepHourly},
		{"--keep-daily", retention.KeepDaily},
		{"--keep-weekly", retention.KeepWeekly},
		{"--keep-monthly", retention.KeepMonthly},
		{"--keep-yearly", retention.KeepYearly},
	} {
		if rule.value != nil {
			args = append(args, rule.flag, strconv.Itoa(*rule.value))
		}
	}
	return append(args, "--", location)
}

// archiveGlob selects the archives created for one archive prefix. The
// timestamp suffix has a fixed width, so the prefix "service" does not match
// archives named for "service-b".
func archiveGlob(prefix string) string {
	return prefix + "-????-??-??T??:??:??"
}

// classifyExit follows Borg's own classification, which holds in both legacy
// and BORG_EXIT_CODES=modern modes. Status 1 and the modern warning range
// 100 through 127 mean the operation completed with warnings. Everything else
// that is not 0 is a failure, including modern error codes and signals.
func classifyExit(verb string, code int) (Status, error) {
	switch {
	case code == 0:
		return Succeeded, nil
	case code == 1 || (code >= 100 && code < 128):
		return Warned, nil
	default:
		return Failed, fmt.Errorf("borg %s exited with status %d", verb, code)
	}
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
