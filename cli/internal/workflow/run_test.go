package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

type runnerFunc func(context.Context, borg.Request) (borg.Result, error)

func (f runnerFunc) Run(ctx context.Context, req borg.Request) (borg.Result, error) {
	return f(ctx, req)
}

func testPlan() Plan {
	task := func(name string) Task {
		return Task{Type: "backup", Repository: name, Scope: "scope", Request: borg.Request{Args: []string{name}}}
	}
	return Plan{Name: "manual", Stages: [][]Task{{task("a"), task("b")}, {task("c")}}}
}

func TestStageConcurrencyAndBarrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan string, 3)
	release := make(chan struct{})
	var finished atomic.Int32
	runner := runnerFunc(func(ctx context.Context, req borg.Request) (borg.Result, error) {
		name := req.Args[0]
		started <- name
		if name == "c" {
			if finished.Load() != 2 {
				return borg.Result{Outcome: borg.Failure}, errors.New("advanced before siblings finished")
			}
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
			}
			finished.Add(1)
		}
		_, err := fmt.Fprintln(req.Diagnostics, "native diagnostic")
		return borg.Result{Outcome: borg.Success}, err
	})
	done := make(chan error, 1)
	// Sharing a non-thread-safe writer across both streams exercises the shared lock.
	var output bytes.Buffer
	go func() {
		result, err := (Executor{Runner: runner, Stdout: &output, Stderr: &output}).Run(ctx, testPlan())
		if err == nil && (result.Outcome != borg.Success || len(result.Stages) != 2) {
			err = fmt.Errorf("unexpected workflow result: %+v", result)
		}
		done <- err
	}()
	for range 2 {
		select {
		case name := <-started:
			if name == "c" {
				t.Fatal("later stage started early")
			}
		case <-ctx.Done():
			t.Fatal("sibling tasks did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"cairn [manual 1.1 backup/a]", "borg [manual 1.1 backup/a] native diagnostic", "completed"} {
		if !strings.Contains(output.String(), text) {
			t.Fatalf("missing %q in %q", text, &output)
		}
	}
}

func TestCancellationRetainsCleanupFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := runnerFunc(func(ctx context.Context, _ borg.Request) (borg.Result, error) {
		cancel()
		return borg.Result{Outcome: borg.Interrupted}, errors.Join(ctx.Err(), borg.ErrProcessGroupNotStopped)
	})
	result, err := (Executor{Runner: runner}).Run(ctx, Plan{Stages: [][]Task{{{Type: "backup"}}}})
	if result.Outcome != borg.Interrupted || !errors.Is(err, context.Canceled) || !errors.Is(err, borg.ErrProcessGroupNotStopped) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestStageOutcomesWaitForSiblings(t *testing.T) {
	for _, outcomes := range [][2]borg.Outcome{
		{borg.Warning, borg.Success}, {borg.Success, borg.Warning},
		{borg.Failure, borg.Success}, {borg.Warning, borg.Failure},
		{borg.Failure, borg.Warning}, {borg.Interrupted, borg.Success},
	} {
		t.Run(fmt.Sprint(outcomes), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			earlyDone := make(chan struct{})
			var siblings atomic.Int32
			runner := runnerFunc(func(ctx context.Context, req borg.Request) (borg.Result, error) {
				name := req.Args[0]
				if name == "c" {
					t.Error("later stage started after warning/failure")
					return borg.Result{Outcome: borg.Success}, nil
				}
				index := 0
				if name == "a" {
					close(earlyDone)
				} else {
					index = 1
					select {
					case <-earlyDone:
					case <-ctx.Done():
						return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
					}
				}
				if ctx.Err() != nil {
					t.Error("ordinary result canceled sibling")
				}
				siblings.Add(1)
				outcome := outcomes[index]
				var err error
				if outcome == borg.Failure || outcome == borg.Interrupted {
					err = errors.New("operation failed")
				}
				return borg.Result{Outcome: outcome, Signal: 9}, err
			})
			result, err := (Executor{Runner: runner}).Run(ctx, testPlan())
			want := borg.Warning
			if outcomes[0] == borg.Failure || outcomes[1] == borg.Failure || outcomes[0] == borg.Interrupted {
				want = borg.Failure
			}
			if result.Outcome != want || len(result.Stages) != 1 || siblings.Load() != 2 || (want == borg.Failure) != (err != nil) {
				t.Fatalf("result=%+v err=%v finished=%d", result, err, siblings.Load())
			}
		})
	}
}

func TestCancellationWaitsForCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	started := make(chan struct{}, 2)
	cleaned := make(chan struct{}, 2)
	release := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, req borg.Request) (borg.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		select {
		case <-release:
		case <-deadline.Done():
		}
		cleaned <- struct{}{}
		return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
	})
	done := make(chan Result, 1)
	go func() {
		result, _ := (Executor{Runner: runner}).Run(ctx, testPlan())
		done <- result
	}()
	for range 2 {
		select {
		case <-started:
		case <-deadline.Done():
			t.Fatal("tasks did not start")
		}
	}
	cancel()
	select {
	case <-done:
		t.Fatal("returned before cleanup")
	default:
	}
	close(release)
	result := <-done
	if result.Outcome != borg.Interrupted || len(result.Stages) != 1 || len(cleaned) != 2 {
		t.Fatalf("result=%+v cleaned=%d", result, len(cleaned))
	}
}

type badWriter struct{ short bool }

func (w badWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("destination failed")
}

func TestOutputFailure(t *testing.T) {
	for _, short := range []bool{false, true} {
		for _, stderr := range []bool{false, true} {
			t.Run(fmt.Sprintf("short=%v stderr=%v", short, stderr), func(t *testing.T) {
				e := Executor{Runner: runnerFunc(func(_ context.Context, req borg.Request) (borg.Result, error) {
					return borg.Result{Outcome: borg.Success}, nil
				})}
				if stderr {
					e.Stderr = badWriter{short: short}
				} else {
					e.Stdout = badWriter{short: short}
				}
				result, err := e.Run(t.Context(), testPlan())
				if result.Outcome != borg.Failure || err == nil || len(result.Stages) != 1 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if short && !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("short write error lost: %v", err)
				}
			})
		}
	}
}

func TestPreCanceledWorkflow(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := (Executor{Runner: runnerFunc(func(context.Context, borg.Request) (borg.Result, error) {
		t.Error("operation started after cancellation")
		return borg.Result{}, nil
	})}).Run(ctx, testPlan())
	if result.Outcome != borg.Interrupted || !errors.Is(err, context.Canceled) || len(result.Stages) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
