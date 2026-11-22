// Package workflow prepares and executes configured workflows without a scheduler
// or command parsing. The standalone operation packages remain config-independent.
package workflow

import (
	"encoding/hex"
	"fmt"
	"io/fs"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/backup"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/check"
	"github.com/Besi97/cairn-backup/cli/internal/compact"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/prune"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/assertions"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/drill"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/selection"
)

// Plan contains prepared operations, not open files or running subprocesses.
// Treat it as sensitive: its requests may contain inline credentials.
type Plan struct {
	Name   string
	Stages [][]Task
}

type Task struct {
	Type       string
	Repository string
	Scope      string
	Request    borg.Request
	Drill      *drill.Options
}

// Prepare adapts a fully loaded configuration to the same primitives used by
// standalone commands. A single supplied time makes archive naming deterministic.
// It does not execute Borg, check path existence, or open credentials.
func Prepare(selected *config.Config, name string, now time.Time) (Plan, error) {
	definition, ok := selected.Workflows[name]
	if !ok {
		return Plan{}, fmt.Errorf("unknown workflow %q", name)
	}
	plan := Plan{Name: name}
	for i, stage := range definition.Stages {
		tasks := make([]Task, 0, len(stage.Tasks))
		for j, task := range stage.Tasks {
			repository, ok := selected.Repositories[task.Repository]
			if !ok {
				return Plan{}, fmt.Errorf("stage %d task %d: unknown repository", i+1, j+1)
			}
			prepared, err := prepareTask(task, repository, selected.Directory, now)
			if err != nil {
				// Preparation errors must not quote configured values.
				return Plan{}, fmt.Errorf("stage %d task %d: cannot prepare %s task", i+1, j+1, task.Type)
			}
			tasks = append(tasks, prepared)
		}
		plan.Stages = append(plan.Stages, tasks)
	}
	return plan, nil
}

func prepareTask(task config.Task, repository config.Repository, directory string, now time.Time) (Task, error) {
	result := Task{Type: task.Type, Repository: task.Repository}
	switch task.Type {
	case "recovery_check":
		repo, err := borg.PrepareRepository(borg.RepositoryOptions{Repository: repository.Location, PassphraseFile: repository.PassphraseFile, IdentityFile: repository.IdentityFile}, directory)
		if err != nil {
			return Task{}, err
		}
		repo.Access.Passphrase = repository.Passphrase
		options := &drill.Options{Repository: repo, Dir: directory, LockWait: task.LockWait, Selector: selection.Selector{Exact: task.ArchiveName, LatestPrefix: task.ArchivePrefix}, Paths: append([]string(nil), task.Paths...), WholeArchive: task.WholeArchive}
		for _, c := range task.Checks {
			check := assertions.Check{Kind: assertions.Kind(c.Kind), Path: c.Path, Size: c.Size, Bytes: []byte(c.Bytes), Count: c.Count}
			switch c.Type {
			case "directory":
				check.Type = fs.ModeDir
			case "symlink":
				check.Type = fs.ModeSymlink
			case "fifo":
				check.Type = fs.ModeNamedPipe
			case "socket":
				check.Type = fs.ModeSocket
			case "block_device":
				check.Type = fs.ModeDevice
			case "char_device":
				check.Type = fs.ModeDevice | fs.ModeCharDevice
			}
			if c.Kind == "file_sha256" {
				digest, err := hex.DecodeString(c.SHA256)
				if err != nil || len(digest) != len(check.Digest) {
					return Task{}, fmt.Errorf("invalid digest")
				}
				copy(check.Digest[:], digest)
			}
			options.Checks = append(options.Checks, check)
		}
		if task.Sample != nil {
			options.Sample = &drill.SampleOptions{LiveRoot: task.Sample.LiveRoot, Limit: task.Sample.Limit}
		}
		result.Drill, result.Scope = options, "recovery drill"
	case "backup":
		prepared, err := backup.Prepare(backup.Options{
			Repository: repository.Location, ArchivePrefix: task.ArchivePrefix,
			ArchiveName: task.ArchiveName, SourceRoot: task.SourceRoot,
			Sources: task.Sources, Excludes: task.Exclude, LockWait: task.LockWait,
			PassphraseFile: repository.PassphraseFile, IdentityFile: repository.IdentityFile,
		}, directory, now)
		if err != nil {
			return Task{}, err
		}
		result.Request, result.Scope = prepared.Request, "archive template: "+prepared.Archive
	case "prune":
		prepared, err := prune.Prepare(prune.Options{
			Repository: repository.Location, ArchivePrefix: task.ArchivePrefix,
			LockWait: task.LockWait, PassphraseFile: repository.PassphraseFile,
			IdentityFile: repository.IdentityFile,
			Retention: prune.Retention{
				Within: task.KeepWithin, Secondly: task.KeepSecondly, Minutely: task.KeepMinutely,
				Hourly: task.KeepHourly, Daily: task.KeepDaily, Weekly: task.KeepWeekly,
				Monthly: task.KeepMonthly, Yearly: task.KeepYearly,
			},
		}, directory)
		if err != nil {
			return Task{}, err
		}
		result.Request, result.Scope = prepared.Request, "archive selector: "+prepared.Selector
	case "compact":
		request, err := compact.Prepare(compact.Options{
			Repository: repository.Location, IdentityFile: repository.IdentityFile,
			LockWait: task.LockWait,
		}, directory)
		if err != nil {
			return Task{}, err
		}
		result.Request, result.Scope = request, "whole repository; reclaiming eligible space"
	case "check":
		request, err := check.Prepare(check.Options{
			Repository: repository.Location, PassphraseFile: repository.PassphraseFile,
			IdentityFile: repository.IdentityFile, LockWait: task.LockWait,
			VerifyData: task.VerifyData,
		}, directory)
		if err != nil {
			return Task{}, err
		}
		result.Request = request
		result.Scope = "whole repository consistency and all archive metadata; file contents are NOT cryptographically verified; no repair"
		if task.VerifyData {
			result.Scope = "whole repository consistency, all archive metadata, and cryptographic file-content verification; may take a long time and transfer substantial remote data; no repair"
		}
		result.Scope += "; Borg warnings can indicate corruption or skipped checks; inspect its diagnostics."
	default:
		return Task{}, fmt.Errorf("unsupported task type")
	}
	if task.Type != "compact" && task.Type != "recovery_check" {
		result.Request.Access.Passphrase = repository.Passphrase
	}
	return result, nil
}
