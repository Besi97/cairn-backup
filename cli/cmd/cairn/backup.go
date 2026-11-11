package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/backup"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

const backupUsage = "usage: cairn backup --job NAME [--config PATH] [--dry-run]\n"

const previewLimits = `Preview limits:
- Backup: Borg lists every source path it would consider. It does not compare
  them with the repository, so it cannot tell new, changed, or unchanged files
  apart, and it cannot show archive size, compression, or deduplication.
- Backup: the preview does not read file contents, so it cannot report
  unreadable files that a real backup would warn about. It does not contact the
  repository, check the passphrase, or check SSH access.
- Prune: the result reflects only archives that exist now. The archive a real
  backup would create is not included, so the real prune may keep or remove
  different archives.
- Prune (when retention is configured): the preview opens the repository, checks
  the passphrase, and waits for the repository lock, like a real prune.
- Compaction is not previewed.
- Later backups and prunes can differ from this preview.
`

func runBackup(args []string, stdout, stderr io.Writer, env config.Environment) int {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	// flag errors can echo arbitrary arguments; use controlled diagnostics.
	flags.SetOutput(io.Discard)
	var job, explicit string
	var dryRun bool
	flags.StringVar(&job, "job", "", "job to run")
	flags.StringVar(&explicit, "config", "", "configuration file")
	flags.BoolVar(&dryRun, "dry-run", false, "preview without changing the repository")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(stdout, backupUsage)
			return 0
		}
		fmt.Fprint(stderr, backupUsage)
		return 2
	}
	set := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if flags.NArg() != 0 || !set["job"] || job == "" || (set["config"] && explicit == "") {
		fmt.Fprint(stderr, backupUsage)
		return 2
	}
	cfg, err := env.LoadValidated(explicit)
	if err != nil {
		fmt.Fprintf(stderr, "cairn: %v\n", err)
		return 3
	}
	if cfg.Jobs[job] == nil {
		fmt.Fprintf(stderr, "cairn: job %q is not configured\n", job)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if dryRun {
		status, err := backup.Preview(ctx, cfg, job, stdout, stderr)
		return reportPreview(job, status, err, stdout, stderr)
	}
	status, err := backup.Run(ctx, cfg, job, stdout, stderr)
	switch status {
	case backup.Succeeded:
		fmt.Fprintf(stdout, "Backup of job %q completed.\n", job)
		return 0
	case backup.Warned:
		fmt.Fprintf(stdout, "Backup of job %q completed with warnings.\n", job)
		return 1
	case backup.Interrupted:
		fmt.Fprintf(stderr, "cairn: backup of job %q interrupted\n", job)
		return 130
	default:
		fmt.Fprintf(stderr, "cairn: backup of job %q failed: %v\n", job, err)
		return 4
	}
}

func reportPreview(job string, status backup.Status, err error, stdout, stderr io.Writer) int {
	switch status {
	case backup.Succeeded:
		fmt.Fprintf(stdout, "Dry run of job %q completed. The repository was not changed.\n", job)
		fmt.Fprint(stdout, previewLimits)
		return 0
	case backup.Warned:
		fmt.Fprintf(stdout, "Dry run of job %q completed with warnings. The repository was not changed.\n", job)
		fmt.Fprint(stdout, previewLimits)
		return 1
	case backup.Interrupted:
		fmt.Fprintf(stderr, "cairn: dry run of job %q interrupted\n", job)
		return 130
	default:
		fmt.Fprintf(stderr, "cairn: dry run of job %q failed: %v\n", job, err)
		return 4
	}
}
