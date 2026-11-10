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

const backupUsage = "usage: cairn backup --job NAME [--config PATH]\n"

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
	if dryRun {
		fmt.Fprintln(stderr, "cairn: --dry-run is not available yet")
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
