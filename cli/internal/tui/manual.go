package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

type manualDone struct{ message string }

type manualRunner struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	active  bool
	message string
	run     func(context.Context, workflow.Manual) (workflow.Result, error)
}

func newManualRunner() *manualRunner {
	return &manualRunner{ctx: context.Background(), run: func(ctx context.Context, manual workflow.Manual) (workflow.Result, error) {
		return manual.Run(ctx, workflow.Executor{Stdout: io.Discard, Stderr: io.Discard})
	}}
}

func (r *manualRunner) start(manual workflow.Manual) tea.Cmd {
	ctx, cancel := context.WithCancel(r.ctx)
	r.cancel, r.done, r.active = cancel, make(chan struct{}), true
	r.message = "RUNNING: q / Ctrl+C cancels and waits."
	done := r.done
	result := make(chan manualDone, 1)
	go func() {
		defer close(done)
		defer cancel()
		outcome, err := r.run(ctx, manual)
		message := "FAILED: Borg diagnostics were not retained; check history."
		switch {
		case errors.Is(err, runlock.ErrBusy):
			message = "BUSY: workflow already running; not queued."
		case outcome.Outcome == borg.Interrupted:
			message = "INTERRUPTED: check history."
		case err == nil && outcome.Outcome == borg.Success:
			message = "SUCCESS: refresh history for details."
		case err == nil && outcome.Outcome == borg.Warning:
			message = "WARNING: Borg diagnostics were not retained."
		}
		result <- manualDone{message}
	}()
	return func() tea.Msg { return <-result }
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
			rows = append(rows, fmt.Sprintf("  %d. %s → %s", j+1, config.DisplayText(task.Type), config.DisplayText(task.Repository)))
		}
	}
	rows = append(rows, "Prune can delete archives; compact reclaims repository space.", "Review all stages. Press y to start; Esc cancels.")
	return strings.Join(rows, "\n\n")
}
