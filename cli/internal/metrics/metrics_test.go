package metrics

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func TestHistoryAndScope(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	id := fmt.Sprintf("%064x", 1)
	c, err := New(id, []string{"daily", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for i := 0; i < history.MaxRuns+1; i++ {
		name := "other"
		if i == 0 {
			name = "daily"
		}
		e := lifecycle.Event{RunID: fmt.Sprint(i), ConfigID: id, Workflow: name, Source: lifecycle.Manual, Sequence: 1, Time: now.Add(time.Duration(i) * time.Second), Kind: lifecycle.RunFinished, Outcome: lifecycle.Success}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	snap := c.Snapshot()
	if !snap.HistoryAvailable || snap.Workflows[0].SuccessKnown || snap.Workflows[0].LastOutcome != "" {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	start := now.Add(200 * time.Second)
	e := lifecycle.Event{RunID: "shared", ConfigID: id, Workflow: "daily", Source: lifecycle.Manual, Sequence: 1, Time: start, Kind: lifecycle.RunStarted}
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	e.Sequence++
	e.Time = start.Add(time.Minute)
	e.Kind = lifecycle.RunFinished
	e.Outcome = lifecycle.Success
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	snap = c.Snapshot()
	w := snap.Workflows[0]
	if !w.SuccessKnown || !w.LastSuccess.Equal(e.Time) || !w.DurationKnown || w.LastDuration != time.Minute || w.Success != 0 {
		t.Fatalf("unexpected workflow: %+v", w)
	}
	if snap.Workflows[1].SuccessKnown {
		t.Fatal("missing workflow should be unknown")
	}
}

func TestConcurrentObserverAndSnapshot(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, _ := New("id", []string{"daily"})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e := lifecycle.Event{RunID: fmt.Sprint(i), ConfigID: "id", Workflow: "daily", Kind: lifecycle.RunStarted, Time: time.Now()}
			c.Observe(e)
			c.Snapshot()
			e.Kind = lifecycle.RunFinished
			e.Outcome = lifecycle.Success
			c.Observe(e)
		}(i)
	}
	wg.Wait()
	w := c.Snapshot().Workflows[0]
	if w.Success != 100 || w.InFlight != 0 || !w.InFlightKnown {
		t.Fatalf("%+v", w)
	}
}

func TestLimitsAndFiltering(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := New("id", make([]string, MaxWorkflows+1)); err == nil {
		t.Fatal("expected limit")
	}
	c, _ := New("id", []string{"daily"})
	for i := 0; i <= MaxActive; i++ {
		c.Observe(lifecycle.Event{RunID: fmt.Sprint(i), ConfigID: "id", Workflow: "daily", Kind: lifecycle.RunStarted})
	}
	c.Observe(lifecycle.Event{ConfigID: "other", Workflow: "daily", Kind: lifecycle.RunSkipped, Outcome: lifecycle.Skipped, Reason: lifecycle.Busy})
	c.Observe(lifecycle.Event{ConfigID: "id", Workflow: "daily", Kind: lifecycle.RunSkipped, Outcome: lifecycle.Skipped, Reason: lifecycle.Busy, Preview: true})
	w := c.Snapshot().Workflows[0]
	if w.InFlightKnown || w.InFlight != MaxActive || w.Busy != 0 {
		t.Fatalf("%+v", w)
	}
}

func TestLifecycleObserver(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	id := fmt.Sprintf("%064x", 1)
	c, _ := New(id, []string{"daily"})
	ctx := lifecycle.WithConfigID(lifecycle.WithAdditionalObserver(t.Context(), c.Observe), id)
	run := lifecycle.New(ctx, lifecycle.Event{Workflow: "daily"})
	run.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	if c.Snapshot().Workflows[0].InFlight != 1 {
		t.Fatal("start not observed")
	}
	run.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
	w := c.Snapshot().Workflows[0]
	if w.Success != 1 || w.InFlight != 0 || !w.DurationKnown || !w.SuccessKnown {
		t.Fatalf("%+v", w)
	}
}

func TestOtherProcessHistory(t *testing.T) {
	id := fmt.Sprintf("%064x", 1)
	if os.Getenv("CAIRN_METRICS_HELPER") == "1" {
		s, err := history.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		err = s.Observe(lifecycle.Event{RunID: "child", ConfigID: id, Workflow: "daily", Source: lifecycle.Manual, Sequence: 1, Time: time.Now(), Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=^TestOtherProcessHistory$")
	cmd.Env = append(os.Environ(), "CAIRN_METRICS_HELPER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v: %s", err, out)
	}
	c, _ := New(id, []string{"daily"})
	w := c.Snapshot().Workflows[0]
	if !w.SuccessKnown || w.Success != 0 || w.InFlight != 0 {
		t.Fatalf("%+v", w)
	}
}
