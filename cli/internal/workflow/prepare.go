// Package workflow prepares and executes configured workflows without a scheduler
// or command parsing. The standalone operation packages remain config-independent.
package workflow

import (
	"fmt"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/backup"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/compact"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/prune"
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
	default:
		return Task{}, fmt.Errorf("unsupported task type")
	}
	if task.Type != "compact" {
		result.Request.Access.Passphrase = repository.Passphrase
	}
	return result, nil
}
