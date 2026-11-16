package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func TestOutputSharedAcrossExecutorsAndReports(t *testing.T) {
	var buffer bytes.Buffer
	output := NewOutput(&buffer, &buffer)
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			name := fmt.Sprintf("workflow-%d", i)
			executor := Executor{Output: output, Runner: runnerFunc(func(_ context.Context, req borg.Request) (borg.Result, error) {
				for range 10 {
					if _, err := fmt.Fprintln(req.Diagnostics, name); err != nil {
						return borg.Result{Outcome: borg.Failure}, err
					}
				}
				return borg.Result{Outcome: borg.Success}, nil
			})}
			result, err := executor.Run(t.Context(), Plan{Name: name, Stages: [][]Task{{
				{Type: "compact", Repository: "local", Scope: name},
			}}})
			if err != nil || result.Outcome != borg.Success {
				t.Errorf("run: %+v %v", result, err)
			}
			fmt.Fprintln(output, "agent report")
		}()
	}
	workers.Wait()
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "agent report" {
			continue
		}
		var index int
		source, _, ok := strings.Cut(line, " ")
		if !ok || (source != "borg" && source != "cairn") {
			t.Fatalf("missing output source: %q", line)
		}
		if _, err := fmt.Sscanf(line, source+` [workflow-%d`, &index); err != nil {
			t.Fatalf("unattributed output: %q", line)
		}
		prefix := fmt.Sprintf(`%s [workflow-%d 1.1 compact/local] `, source, index)
		if line != prefix+fmt.Sprintf("workflow-%d", index) && line != prefix+"completed" {
			t.Fatalf("interleaved label/payload: %q", line)
		}
	}
}

func TestTaskCountsAndSkipReasonsUseEvents(t *testing.T) {
	for _, tt := range []struct {
		name    string
		outcome borg.Outcome
		counts  TaskCounts
		reason  string
	}{
		{"success", borg.Success, TaskCounts{Success: 3}, ""},
		{"warning", borg.Warning, TaskCounts{Success: 1, Warning: 1, Skipped: 1}, "previous stage completed with warnings"},
		{"failure", borg.Failure, TaskCounts{Success: 1, Failed: 1, Skipped: 1}, "previous stage failed"},
		{"child signal", borg.Interrupted, TaskCounts{Success: 1, Interrupted: 1, Skipped: 1}, "previous stage failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var diagnostics bytes.Buffer
			var events []lifecycle.Event
			ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
				events = append(events, event)
				return errors.New("private observer failure")
			})
			executor := Executor{Stderr: &diagnostics, Runner: runnerFunc(func(_ context.Context, req borg.Request) (borg.Result, error) {
				outcome := borg.Success
				if req.Args[0] == "b" {
					outcome = tt.outcome
				}
				var err error
				if outcome == borg.Failure || outcome == borg.Interrupted {
					err = errors.New("operation failed")
				}
				return borg.Result{Outcome: outcome, Signal: 9}, err
			})}
			result, _ := executor.Run(ctx, testPlan())
			if result.Tasks != tt.counts {
				t.Fatalf("counts=%+v want=%+v", result.Tasks, tt.counts)
			}
			if tt.reason != "" && !strings.Contains(diagnostics.String(), "cairn [manual 2.1 backup/c] skipped: "+tt.reason) {
				t.Fatalf("missing skip reason: %q", diagnostics.String())
			}
			if len(events) == 0 || events[len(events)-1].Kind != lifecycle.RunFinished || lifecycle.Err(ctx) != lifecycle.ErrDelivery {
				t.Fatalf("caller observer lost: events=%+v error=%v", events, lifecycle.Err(ctx))
			}
			if tt.outcome == borg.Success && result.Outcome != borg.Success {
				t.Fatal("observer failure changed operation outcome")
			}
		})
	}
}

func TestCancellationCountsUnstartedTasksAsSkipped(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var diagnostics bytes.Buffer
	result, err := (Executor{Stderr: &diagnostics}).Run(ctx, testPlan())
	if !errors.Is(err, context.Canceled) || result.Tasks != (TaskCounts{Skipped: 3}) {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if strings.Count(diagnostics.String(), "skipped: cancelled\n") != 3 {
		t.Fatalf("missing cancellation skips: %q", diagnostics.String())
	}
}
