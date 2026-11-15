package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

// Clock adjustments change wall time independently of the monotonic test timer.
// synctest advances timers without waiting for real cron minutes.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func (*testClock) NewTimer(d time.Duration) timer { return wallClock{}.NewTimer(d) }

func (c *testClock) step(now time.Time) {
	c.set(now)
	time.Sleep(time.Second)
	synctest.Wait()
}

func loadTestConfig(t *testing.T, definitions string) *config.Config {
	t.Helper()
	runtime := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	text := "version: 1\nrepositories:\n  local: {location: ./repo}\nworkflows:\n" + definitions
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: base})
	if err != nil {
		t.Fatal(err)
	}
	return selected
}

func definition(name, cron, timezone string) string {
	schedule := ""
	if cron != "" {
		schedule += fmt.Sprintf("    cron: %q\n", cron)
	}
	if timezone != "" {
		schedule += fmt.Sprintf("    timezone: %q\n", timezone)
	}
	return fmt.Sprintf("  %s:\n%s    stages: [{tasks: [{type: compact, repository: local}]}]\n", name, schedule)
}

func newTestAgent(t *testing.T, definitions string, now time.Time) (*Agent, *testClock) {
	t.Helper()
	a, err := New(loadTestConfig(t, definitions))
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: now}
	a.clock = clock
	return a, clock
}

func begin(t *testing.T, a *Agent, stdout, stderr io.Writer) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, stdout, stderr) }()
	synctest.Wait()
	return cancel, done
}

func stop(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	synctest.Wait()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
}

func instant(value string) time.Time {
	now, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return now
}

func TestClockDispatch(t *testing.T) {
	for _, tt := range []struct {
		name, cron, zone, start string
		steps                   []string
		counts                  []int
	}{
		{"minute boundaries", "* * * * *", "UTC", "2026-01-01T00:00:00Z",
			[]string{"2026-01-01T00:00:59Z", "2026-01-01T00:01:00Z", "2026-01-01T00:01:59Z", "2026-01-01T00:02:00Z"}, []int{0, 1, 1, 2}},
		{"late within minute", "* * * * *", "UTC", "2026-01-01T00:00:10Z",
			[]string{"2026-01-01T00:01:59Z"}, []int{1}},
		{"jump to current minute", "* * * * *", "UTC", "2026-01-01T00:00:00Z",
			[]string{"2026-01-02T01:03:20Z", "2026-01-02T01:03:59Z"}, []int{1, 1}},
		{"expired minute", "1 * * * *", "UTC", "2026-01-01T00:00:00Z",
			[]string{"2026-01-01T00:02:00Z", "2026-01-01T00:01:00Z", "2026-01-01T01:01:00Z"}, []int{0, 0, 1}},
		{"backward no replay", "* * * * *", "UTC", "2026-01-01T00:00:00Z",
			[]string{"2026-01-01T00:01:00Z", "2026-01-01T00:00:30Z", "2026-01-01T00:01:00Z", "2026-01-01T00:02:00Z"}, []int{1, 1, 1, 2}},
		{"explicit zone", "0 9 * * *", "Asia/Kolkata", "2026-01-01T03:29:30Z",
			[]string{"2026-01-01T03:30:00Z"}, []int{1}},
		{"DST gap", "30 2 * * *", "Europe/Berlin", "2026-03-28T02:00:00Z",
			[]string{"2026-03-29T01:30:00Z", "2026-03-30T00:30:00Z"}, []int{0, 1}},
		{"DST repeat", "30 2 * * *", "Europe/Berlin", "2026-10-25T00:00:00Z",
			[]string{"2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"}, []int{1, 2}},
		{"impossible", "0 0 30 2 *", "UTC", "2026-01-01T00:00:00Z",
			[]string{"2026-02-01T00:00:00Z", "2027-01-01T00:00:00Z"}, []int{0, 0}},
		{"bounded horizon recheck", "0 0 29 2 *", "UTC", "2097-03-01T00:00:00Z",
			[]string{"2100-03-01T00:00:00Z", "2104-02-29T00:00:00Z"}, []int{0, 1}},
		{"bounded horizon forward jump", "0 0 29 2 *", "UTC", "2097-03-01T00:00:00Z",
			[]string{"2104-02-29T00:00:00Z"}, []int{1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, clock := newTestAgent(t, definition("nightly", tt.cron, tt.zone), instant(tt.start))
				count := 0
				a.execute = func(context.Context, workflow.Plan) (workflow.Result, error) {
					count++
					return workflow.Result{Outcome: borg.Success}, nil
				}
				cancel, done := begin(t, a, io.Discard, io.Discard)
				if count != 0 {
					t.Fatal("startup dispatched work")
				}
				for i, step := range tt.steps {
					clock.step(instant(step))
					if count != tt.counts[i] {
						t.Fatalf("at %s count=%d, want %d", step, count, tt.counts[i])
					}
				}
				stop(t, cancel, done)
			})
		})
	}
}

func TestIndependentWorkflowsAndOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, definition("first", "* * * * *", "UTC")+
			definition("second", "* * * * *", "UTC")+definition("manual", "", ""), instant("2026-01-01T00:00:00Z"))
		calls := map[string]int{}
		release := make(chan struct{})
		a.execute = func(ctx context.Context, plan workflow.Plan) (workflow.Result, error) {
			// Several dispatches may enter concurrently.
			clock.mu.Lock()
			calls[plan.Name]++
			clock.mu.Unlock()
			if plan.Name == "first" {
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			return workflow.Result{Outcome: borg.Success}, nil
		}
		var output bytes.Buffer
		cancel, done := begin(t, a, &output, &output)
		clock.step(instant("2026-01-01T00:01:00Z"))
		clock.step(instant("2026-01-01T00:02:00Z"))
		if calls["first"] != 1 || calls["second"] != 2 || calls["manual"] != 0 {
			t.Fatalf("calls=%v", calls)
		}
		if !strings.Contains(output.String(), `workflow "first" skipped: already running`) {
			t.Fatal("missing overlap report:", &output)
		}
		close(release)
		synctest.Wait()
		clock.step(instant("2026-01-01T00:02:20Z"))
		if calls["first"] != 1 {
			t.Fatal("overlap was queued")
		}
		clock.step(instant("2026-01-01T00:03:00Z"))
		if calls["first"] != 2 || calls["second"] != 3 {
			t.Fatalf("later occurrences: %v", calls)
		}
		stop(t, cancel, done)
	})
}

func TestRunOutcomesDoNotRetryOrStopAgent(t *testing.T) {
	for _, tt := range []struct {
		name    string
		outcome borg.Outcome
		err     error
		message string
	}{
		{"warning", borg.Warning, nil, "completed with warnings"},
		{"failure", borg.Failure, errors.New("operation failed"), "failed"},
		{"busy", borg.Failure, runlock.ErrBusy, "skipped: already running"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, clock := newTestAgent(t, definition("nightly", "* * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
				count := 0
				a.execute = func(context.Context, workflow.Plan) (workflow.Result, error) {
					count++
					return workflow.Result{Outcome: tt.outcome}, tt.err
				}
				var output bytes.Buffer
				cancel, done := begin(t, a, &output, &output)
				for _, value := range []string{"2026-01-01T00:01:00Z", "2026-01-01T00:01:10Z", "2026-01-01T00:00:30Z", "2026-01-01T00:01:00Z"} {
					clock.step(instant(value))
				}
				if count != 1 || !strings.Contains(output.String(), tt.message) {
					t.Fatalf("count=%d output=%s", count, &output)
				}
				clock.step(instant("2026-01-01T00:02:00Z"))
				if count != 2 {
					t.Fatal("agent stopped after run outcome")
				}
				stop(t, cancel, done)
			})
		})
	}
}

func TestShutdownWaitsForCleanupAndHoldsAgentLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, definition("nightly", "* * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
		cleanup := make(chan struct{})
		calls := 0
		a.execute = func(ctx context.Context, _ workflow.Plan) (workflow.Result, error) {
			calls++
			<-ctx.Done()
			<-cleanup
			return workflow.Result{Outcome: borg.Interrupted}, ctx.Err()
		}
		cancel, done := begin(t, a, io.Discard, io.Discard)
		clock.step(instant("2026-01-01T00:01:00Z"))
		cancel()
		synctest.Wait()
		clock.step(instant("2026-01-01T00:02:00Z"))
		select {
		case err := <-done:
			t.Fatalf("returned before cleanup: %v", err)
		default:
		}
		id, err := runlock.Config(a.selected.Path)
		if err != nil {
			t.Fatal(err)
		}
		store, err := runlock.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if lock, err := store.Agent(id); !errors.Is(err, runlock.ErrBusy) {
			if lock != nil {
				lock.Close()
			}
			t.Fatalf("agent lock released before cleanup: %v", err)
		}
		close(cleanup)
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("shutdown: %v; calls=%d", err, calls)
		}
		lock, err := store.Agent(id)
		if err != nil {
			t.Fatal(err)
		}
		lock.Close()
	})
}

type failingWriter struct{ short bool }

func (w failingWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("output failed")
}

func TestOutputFailureStopsAgent(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprint(short), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				a, _ := newTestAgent(t, definition("nightly", "* * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
				a.execute = func(context.Context, workflow.Plan) (workflow.Result, error) {
					t.Error("dispatched with broken output")
					return workflow.Result{}, nil
				}
				err := a.Run(t.Context(), io.Discard, failingWriter{short: short})
				if err == nil || !strings.Contains(err.Error(), "agent output") {
					t.Fatal("output failure:", err)
				}
			})
		})
	}
}

func TestSnapshotAndDispatchArchiveTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, `  nightly:
    cron: "* * * * *"
    stages: [{tasks: [{type: backup, repository: local, archive_prefix: docs, sources: [payload]}]}]
`, instant("2026-01-01T00:00:00Z"))
		var archives []string
		a.execute = func(_ context.Context, plan workflow.Plan) (workflow.Result, error) {
			archives = append(archives, strings.Join(plan.Stages[0][0].Request.Args, " "))
			return workflow.Result{Outcome: borg.Success}, nil
		}
		cancel, done := begin(t, a, io.Discard, io.Discard)
		if err := os.WriteFile(a.selected.Path, []byte("invalid replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		clock.step(instant("2026-01-01T00:01:12Z"))
		clock.step(instant("2026-01-01T00:02:05Z"))
		if len(archives) != 2 || !strings.Contains(archives[0], "docs-20260101T000112.") ||
			!strings.Contains(archives[1], "docs-20260101T000205.") {
			t.Fatalf("archives=%v", archives)
		}
		stop(t, cancel, done)
	})
}

type switchWriter struct{ failed atomic.Bool }

func (w *switchWriter) Write(p []byte) (int, error) {
	if w.failed.Load() {
		return 0, errors.New("output disconnected")
	}
	return len(p), nil
}

func TestOutputFailureCancelsActiveWorkAndWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, definition("nightly", "* * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
		cancelled, cleanup := make(chan struct{}), make(chan struct{})
		a.execute = func(ctx context.Context, _ workflow.Plan) (workflow.Result, error) {
			<-ctx.Done()
			close(cancelled)
			<-cleanup
			return workflow.Result{Outcome: borg.Interrupted}, ctx.Err()
		}
		writer := &switchWriter{}
		cancel, done := begin(t, a, writer, writer)
		defer cancel()
		clock.step(instant("2026-01-01T00:01:00Z"))
		writer.failed.Store(true)
		// Reporting the next overlap must cancel the previous active run.
		clock.step(instant("2026-01-01T00:02:00Z"))
		select {
		case <-cancelled:
		default:
			t.Fatal("output failure did not cancel active work")
		}
		select {
		case err := <-done:
			t.Fatalf("returned before cleanup: %v", err)
		default:
		}
		close(cleanup)
		synctest.Wait()
		if err := <-done; err == nil || !strings.Contains(err.Error(), "output disconnected") {
			t.Fatalf("output error lost: %v", err)
		}
	})
}

func TestLocalTimezoneCapturedAtStartup(t *testing.T) {
	original := time.Local
	t.Cleanup(func() { time.Local = original })
	time.Local = time.FixedZone("test local", 5*60*60+30*60)
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, definition("nightly", "0 9 * * *", ""), instant("2026-01-01T03:29:00Z"))
		count := 0
		a.execute = func(context.Context, workflow.Plan) (workflow.Result, error) {
			count++
			return workflow.Result{Outcome: borg.Success}, nil
		}
		cancel, done := begin(t, a, io.Discard, io.Discard)
		clock.step(instant("2026-01-01T03:30:00Z"))
		if count != 1 {
			t.Fatal("agent did not use process local timezone")
		}
		stop(t, cancel, done)
	})
}

func TestNoSchedulesAndPreCancelledStartup(t *testing.T) {
	if _, err := New(loadTestConfig(t, definition("manual", "", ""))); !errors.Is(err, ErrNoSchedules) {
		t.Fatalf("manual-only config: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// Cancellation wins before canonical identity resolution or any lock IO.
	if err := (&Agent{}).Run(ctx, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled startup: %v", err)
	}
}

func TestIndependentConfigAgents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _ := newTestAgent(t, definition("nightly", "0 0 30 2 *", "UTC"), instant("2026-01-01T00:00:00Z"))
		copyConfig := *a.selected
		copyConfig.Path = filepath.Join(a.selected.Directory, "copy.yaml")
		data, err := os.ReadFile(a.selected.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copyConfig.Path, data, 0600); err != nil {
			t.Fatal(err)
		}
		second, err := New(&copyConfig)
		if err != nil {
			t.Fatal(err)
		}
		cancelFirst, firstDone := begin(t, a, io.Discard, io.Discard)
		cancelSecond, secondDone := begin(t, second, io.Discard, io.Discard)
		select {
		case err := <-secondDone:
			t.Fatalf("independent agent rejected: %v", err)
		default:
		}
		stop(t, cancelFirst, firstDone)
		stop(t, cancelSecond, secondDone)
	})
}

type clockJumpWriter struct {
	clock  *testClock
	jumped bool
}

func (w *clockJumpWriter) Write(p []byte) (int, error) {
	if !w.jumped && strings.Contains(string(p), " dispatching ") {
		w.jumped = true
		w.clock.set(instant("2026-01-01T00:02:00Z"))
	}
	return len(p), nil
}

func TestOccurrenceExpiresBeforeExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, clock := newTestAgent(t, definition("nightly", "1 * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
		calls := 0
		a.execute = func(context.Context, workflow.Plan) (workflow.Result, error) {
			calls++
			return workflow.Result{Outcome: borg.Success}, nil
		}
		writer := &clockJumpWriter{clock: clock}
		cancel, done := begin(t, a, writer, writer)
		clock.step(instant("2026-01-01T00:01:00Z"))
		if calls != 0 || !writer.jumped {
			t.Fatal("expired occurrence was executed")
		}
		clock.step(instant("2026-01-01T01:01:00Z"))
		if calls != 1 {
			t.Fatal("later occurrence did not execute")
		}
		stop(t, cancel, done)
	})
}
