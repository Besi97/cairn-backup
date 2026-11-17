package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Besi97/cairn-backup/cli/internal/display"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

type TaskCounts struct {
	Success, Warning, Failed, Interrupted, Skipped int
}

func (c TaskCounts) String() string {
	var parts []string
	for _, item := range []struct {
		count int
		label string
	}{
		{c.Success, "succeeded"}, {c.Warning, "warning"}, {c.Failed, "failed"},
		{c.Interrupted, "interrupted"}, {c.Skipped, "skipped"},
	} {
		if item.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.count, item.label))
		}
	}
	if len(parts) == 0 {
		return "tasks: 0"
	}
	return "tasks: " + strings.Join(parts, ", ")
}

type presentation struct {
	mu     sync.Mutex
	counts TaskCounts
	output *Output
	err    error
	runID  string
}

func (e *Executor) presentation(ctx context.Context) (context.Context, *presentation) {
	if e.Output == nil {
		e.Output = NewOutput(e.Stdout, e.Stderr)
	}
	display.AttachDiagnostics(ctx, e.Output)
	p := &presentation{output: e.Output}
	return lifecycle.WithAdditionalObserver(ctx, p.observe), p
}

func taskAttribution(workflow string, stage, task int, operation, repository string) string {
	return display.Attribution(lifecycle.Event{
		Workflow: workflow, Stage: stage, Task: task, Operation: operation, Repository: repository,
	})
}

func (p *presentation) observe(event lifecycle.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.output.Observe(event)
	switch event.Kind {
	case lifecycle.RunStarted:
		p.runID = event.RunID
	case lifecycle.TaskFinished:
		switch event.Outcome {
		case lifecycle.Success:
			p.counts.Success++
		case lifecycle.Warning:
			p.counts.Warning++
		case lifecycle.Failure:
			p.counts.Failed++
		case lifecycle.Interrupted:
			p.counts.Interrupted++
		}
	case lifecycle.TaskSkipped:
		p.counts.Skipped++
		label := display.Label("cairn", display.Attribution(event))
		if _, err := fmt.Fprintf(p.output.Writer(false, label),
			"skipped: %s\n", skipReason(event.Reason)); err != nil && p.err == nil {
			p.err = err
		}
	}
	return nil
}

func skipReason(reason lifecycle.Reason) string {
	switch reason {
	case lifecycle.Busy:
		return "workflow already running"
	case lifecycle.Expired:
		return "scheduled occurrence expired"
	case lifecycle.PriorWarning:
		return "previous stage completed with warnings"
	case lifecycle.PriorFailure:
		return "previous stage failed"
	case lifecycle.Cancelled:
		return "cancelled"
	case lifecycle.PreparationFailed:
		return "workflow preparation failed"
	case lifecycle.Unavailable:
		return "execution unavailable"
	default:
		return string(reason)
	}
}
