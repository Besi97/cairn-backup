package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

type eventRunner func(context.Context, borg.Request) (borg.Result, error)

func (f eventRunner) Run(ctx context.Context, request borg.Request) (borg.Result, error) {
	return f(ctx, request)
}

func TestScheduledLifecycleAndSkips(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		definition := strings.Replace(definition("nightly", "* * * * *", "UTC"),
			"type: compact", "type: check, verify_data: true", 1)
		a, clock := newTestAgent(t, definition, instant("2026-01-01T00:00:00Z"))
		release := make(chan struct{})
		a.execute = func(ctx context.Context, plan workflow.Plan) (workflow.Result, error) {
			return (workflow.Executor{Runner: eventRunner(func(_ context.Context, request borg.Request) (borg.Result, error) {
				if request.Args[0] != "check" || !slices.Contains(request.Args, "--verify-data") {
					t.Errorf("unexpected scheduled check request: %v", request.Args)
				}
				<-release
				return borg.Result{Outcome: borg.Success}, nil
			})}).Run(ctx, plan)
		}
		var events []lifecycle.Event
		ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
			events = append(events, event)
			return errors.New("observer unavailable")
		})
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx, io.Discard, io.Discard) }()
		synctest.Wait()
		clock.step(instant("2026-01-01T00:01:00Z"))
		clock.step(instant("2026-01-01T00:02:00Z"))
		clock.step(instant("2026-01-01T00:04:00Z"))
		close(release)
		synctest.Wait()
		clock.step(instant("2026-01-01T00:05:00Z"))
		stop(t, cancel, done)

		started, finished, busy, expired, skippedTasks := 0, 0, 0, 0, 0
		ids := map[string]bool{}
		for _, event := range events {
			if event.Source != lifecycle.Scheduled || event.ScheduledAt.IsZero() || event.Workflow != "nightly" {
				t.Fatalf("missing scheduled attribution: %+v", event)
			}
			if event.Task != 0 && (event.Operation != "check" || event.Repository != "local") {
				t.Fatalf("missing check attribution: %+v", event)
			}
			switch event.Kind {
			case lifecycle.RunStarted:
				started++
				ids[event.RunID] = true
			case lifecycle.RunFinished:
				finished++
				if event.Outcome != lifecycle.Success {
					t.Fatalf("delivery changed outcome: %+v", event)
				}
			case lifecycle.TaskSkipped:
				skippedTasks++
			case lifecycle.RunSkipped:
				ids[event.RunID] = true
				switch event.Reason {
				case lifecycle.Busy:
					busy++
				case lifecycle.Expired:
					expired++
					if !event.ScheduledAt.Equal(instant("2026-01-01T00:03:00Z")) {
						t.Fatalf("wrong expired occurrence: %+v", event)
					}
				default:
					t.Fatalf("unexpected skip: %+v", event)
				}
			}
		}
		if started != 2 || finished != 2 || busy != 2 || expired != 1 || skippedTasks != 3 || len(ids) != 5 {
			t.Fatalf("started=%d finished=%d busy=%d expired=%d skipped tasks=%d ids=%d",
				started, finished, busy, expired, skippedTasks, len(ids))
		}
	})
}
