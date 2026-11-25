package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

type manualDone struct{ message string }
type manualProgress struct{ message string }

type manualRunner struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	active  bool
	message string
	name    string
	run     func(context.Context, workflow.Manual) (workflow.Result, error)
}

func newManualRunner() *manualRunner {
	return &manualRunner{ctx: context.Background(), run: func(ctx context.Context, manual workflow.Manual) (workflow.Result, error) {
		return manual.Run(ctx, workflow.Executor{Stdout: io.Discard, Stderr: io.Discard})
	}}
}

func (r *manualRunner) start(manual workflow.Manual) tea.Cmd {
	ctx, cancel := context.WithCancelCause(r.ctx)
	stop := func() { cancel(cancellation.Signal(syscall.SIGINT)) }
	r.cancel, r.done, r.active = stop, make(chan struct{}), true
	r.name = config.DisplayText(manual.Plan.Name)
	r.message = "RUNNING: q / Ctrl+C cancels and waits."
	done := r.done
	result := make(chan manualDone, 1)
	progress := make(chan manualProgress, 1)
	go func() {
		defer close(done)
		defer stop()
		ctx = lifecycle.WithAdditionalObserver(ctx, func(event lifecycle.Event) error {
			message := progressMessage(event)
			if message != "" {
				select {
				case progress <- manualProgress{message}:
				default:
					select {
					case <-progress:
					default:
					}
					select {
					case progress <- manualProgress{message}:
					default:
					}
				}
			}
			return nil
		})
		outcome, err := r.run(ctx, manual)
		message := "FAILED: Borg diagnostics were not retained; check history."
		switch {
		case errors.Is(err, runlock.ErrBusy):
			message = "BUSY: workflow already running; not queued."
		case outcome.Outcome == borg.Interrupted:
			message = "INTERRUPTED: check history."
		case err == nil && outcome.Outcome == borg.Success:
			message = "SUCCESS: see History for details."
		case err == nil && outcome.Outcome == borg.Warning:
			message = "WARNING: Borg diagnostics were not retained."
		}
		result <- manualDone{message}
	}()
	return waitManual(result, progress)
}

func waitManual(result <-chan manualDone, progress <-chan manualProgress) tea.Cmd {
	return func() tea.Msg {
		select {
		case done := <-result:
			return done
		default:
		}
		select {
		case done := <-result:
			return done
		case update := <-progress:
			return manualProgressWait{manualProgress: update, next: waitManual(result, progress)}
		}
	}
}

type manualProgressWait struct {
	manualProgress
	next tea.Cmd
}

func progressMessage(event lifecycle.Event) string {
	switch event.Kind {
	case lifecycle.RunStarted:
		return "RUNNING: workflow started."
	case lifecycle.StageStarted:
		return fmt.Sprintf("RUNNING: stage %d started.", event.Stage)
	case lifecycle.StageFinished, lifecycle.StageSkipped:
		return fmt.Sprintf("RUNNING: stage %d %s.", event.Stage, config.DisplayText(string(event.Outcome)))
	case lifecycle.TaskStarted:
		return fmt.Sprintf("RUNNING: stage %d task %d started.", event.Stage, event.Task)
	case lifecycle.TaskFinished, lifecycle.TaskSkipped:
		return fmt.Sprintf("RUNNING: stage %d task %d %s.", event.Stage, event.Task, config.DisplayText(string(event.Outcome)))
	}
	return ""
}

func (r *manualRunner) close() {
	if r.done != nil {
		r.cancel()
		<-r.done
	}
}

func (m Model) confirmation() string {
	plan := m.prepared.Plan
	rows := []string{"y: confirm start · Esc: cancel · ↑↓: scroll",
		section("Workflow", config.DisplayText(plan.Name)), "Stages run in order; tasks in each stage run concurrently."}
	for i, stage := range plan.Stages {
		rows = append(rows, heading(fmt.Sprintf("Stage %d", i+1)))
		for j, task := range stage {
			if task.Command != nil {
				rows = append(rows, fmt.Sprintf("  %d. command (trusted executable)", j+1))
			} else {
				rows = append(rows, fmt.Sprintf("  %d. %s → %s", j+1, config.DisplayText(task.Type), config.DisplayText(task.Repository)))
			}
		}
	}
	rows = append(rows, "Commands run with Cairn's permissions. Prune can delete archives; compact reclaims repository space.", "Review all stages. Press y to start; Esc cancels.")
	return strings.Join(rows, "\n\n")
}
