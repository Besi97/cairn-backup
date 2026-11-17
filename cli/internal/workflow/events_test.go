package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestLifecycleOutcomesAndSkippedStages(t *testing.T) {
	for _, outcome := range []borg.Outcome{borg.Success, borg.Warning, borg.Failure} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			var events []lifecycle.Event
			ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
				events = append(events, event)
				return errors.New("observer secret")
			})
			plan := testPlan()
			secret := "inline secret"
			plan.Stages[0][0].Request.Access.Passphrase = &secret
			executor := Executor{Runner: runnerFunc(func(_ context.Context, request borg.Request) (borg.Result, error) {
				fmt.Fprintln(request.Diagnostics, "raw Borg diagnostics")
				return borg.Result{Outcome: outcome}, nil
			})}
			result, err := executor.Run(ctx, plan)
			if result.Outcome != outcome || err != nil || lifecycle.Err(ctx) == nil {
				t.Fatalf("result=%+v error=%v delivery=%v", result, err, lifecycle.Err(ctx))
			}
			if events[0].Kind != lifecycle.RunStarted || events[len(events)-1].Kind != lifecycle.RunFinished ||
				events[len(events)-1].Outcome != lifecycle.FromBorg(outcome) {
				t.Fatalf("run boundaries: %+v", events)
			}
			starts, finishes, skippedStages, skippedTasks := 0, 0, 0, 0
			for _, event := range events {
				switch event.Kind {
				case lifecycle.TaskStarted:
					starts++
				case lifecycle.TaskFinished:
					finishes++
					if event.Stage == 0 || event.Task == 0 || event.Repository == "" || event.Operation != "backup" {
						t.Fatalf("task attribution: %+v", event)
					}
				case lifecycle.StageSkipped:
					skippedStages++
				case lifecycle.TaskSkipped:
					skippedTasks++
					want := lifecycle.PriorFailure
					if outcome == borg.Warning {
						want = lifecycle.PriorWarning
					}
					if event.Reason != want || event.Stage != 2 || event.Task != 1 {
						t.Fatalf("skip attribution: %+v", event)
					}
				}
			}
			wantTasks, wantSkipped := 2, 1
			if outcome == borg.Success {
				wantTasks, wantSkipped = 3, 0
			}
			if starts != wantTasks || finishes != wantTasks || skippedStages != wantSkipped || skippedTasks != wantSkipped {
				t.Fatalf("starts=%d finishes=%d skipped stages/tasks=%d/%d", starts, finishes, skippedStages, skippedTasks)
			}
			data, _ := json.Marshal(events)
			for _, secret := range []string{"inline secret", "observer secret", "raw Borg diagnostics", "scope"} {
				if strings.Contains(string(data), secret) {
					t.Fatalf("events contain %q", secret)
				}
			}
		})
	}
}

func TestLockedBusyLifecycle(t *testing.T) {
	base := t.TempDir()
	runtime := filepath.Join(base, "runtime")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path := filepath.Join(base, "config")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(identity, testPlan().Name)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	_, err = (Executor{}).RunLocked(ctx, identity, testPlan())
	if !errors.Is(err, runlock.ErrBusy) || len(events) != 6 {
		t.Fatalf("error=%v events=%+v", err, events)
	}
	for _, event := range events {
		if event.Reason != lifecycle.Busy || event.Outcome != lifecycle.Skipped {
			t.Fatalf("busy run started work: %+v", event)
		}
		if event.ConfigID != identity.Digest() {
			t.Fatalf("missing configuration identity: %+v", event)
		}
	}
	if events[len(events)-1].Kind != lifecycle.RunSkipped {
		t.Fatalf("missing terminal skip: %+v", events)
	}
}

func TestLifecycleLockSetupFailure(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	result, err := (Executor{}).RunLocked(ctx, runlock.Identity{}, testPlan())
	if err == nil || result.Outcome != borg.Failure || len(events) != 7 {
		t.Fatalf("result=%+v error=%v events=%+v", result, err, events)
	}
	for _, event := range events[1 : len(events)-1] {
		if event.Outcome != lifecycle.Skipped || event.Reason != lifecycle.Unavailable {
			t.Fatalf("wrong setup failure attribution: %+v", event)
		}
	}
}

func TestLifecyclePrecancelledSkipsEveryTask(t *testing.T) {
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	result, err := (Executor{}).Run(ctx, testPlan())
	if result.Outcome != borg.Interrupted || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	tasks := 0
	for _, event := range events {
		if event.Kind == lifecycle.TaskSkipped {
			tasks++
			if event.Reason != lifecycle.Cancelled {
				t.Fatalf("wrong skip reason: %+v", event)
			}
		}
	}
	if tasks != 3 || events[len(events)-1].Outcome != lifecycle.Interrupted {
		t.Fatalf("events=%+v", events)
	}
}

func TestLifecycleCancellationFinishesActiveTasksBeforeRun(t *testing.T) {
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []lifecycle.Event
	ctx = lifecycle.WithObserver(ctx, func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	executor := Executor{Runner: runnerFunc(func(ctx context.Context, request borg.Request) (borg.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		<-release
		return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		executor.Run(ctx, testPlan())
	}()
	for range 2 {
		select {
		case <-started:
		case <-deadline.Done():
			t.Fatal("tasks did not start")
		}
	}
	cancel()
	close(release)
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("cancellation did not finish")
	}
	finished, skipped := 0, 0
	for _, event := range events {
		switch event.Kind {
		case lifecycle.TaskFinished:
			finished++
			if event.Outcome != lifecycle.Interrupted {
				t.Fatalf("task=%+v", event)
			}
		case lifecycle.StageFinished:
			if finished != 2 || event.Outcome != lifecycle.Interrupted {
				t.Fatalf("stage finished before task cleanup: %+v", events)
			}
		case lifecycle.TaskSkipped:
			skipped++
			if event.Reason != lifecycle.Cancelled {
				t.Fatalf("skip=%+v", event)
			}
		}
	}
	if finished != 2 || skipped != 1 || events[len(events)-1].Outcome != lifecycle.Interrupted {
		t.Fatalf("events=%+v", events)
	}
}
