package history

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func event(id string, seq uint64, kind lifecycle.Kind) lifecycle.Event {
	return lifecycle.Event{RunID: id, Sequence: seq, Time: time.Now().UTC(), Source: lifecycle.Manual, Kind: kind}
}

func store(t *testing.T) (*Store, string) {
	t.Helper()
	state := t.TempDir()
	s, err := open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, filepath.Join(state, "cairn", "history")
}

func TestSummaries(t *testing.T) {
	s, _ := store(t)
	id := rand.Text()
	start := event(id, 1, lifecycle.RunStarted)
	start.Workflow, start.ConfigID = "daily", strings.Repeat("a", 64)
	start.Source, start.ScheduledAt = lifecycle.Scheduled, start.Time.Add(-time.Minute)
	for i, kind := range []lifecycle.Kind{lifecycle.RunStarted, lifecycle.TaskFinished, lifecycle.TaskSkipped, lifecycle.RunFinished} {
		e := start
		e.Sequence, e.Kind = uint64(i+1), kind
		if i == 1 {
			e.Outcome = lifecycle.Warning
		}
		if i == 2 {
			e.Outcome, e.Reason = lifecycle.Skipped, lifecycle.PriorWarning
		}
		if i == 3 {
			e.Outcome = lifecycle.Warning
		}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	r := runs[0]
	if r.Incomplete || r.Partial || r.StartedAt == nil || r.FinishedAt == nil || r.Tasks.Warning != 1 || r.Tasks.Skipped != 1 ||
		r.Outcome != lifecycle.Warning || r.ConfigID != start.ConfigID || r.Workflow != "daily" ||
		r.Source != lifecycle.Scheduled || !r.ScheduledAt.Equal(start.ScheduledAt) {
		t.Fatalf("%+v", r)
	}
	if err := s.Observe(start); err != nil {
		t.Fatal(err)
	}
	again, _ := s.List()
	if again[0].Sequence != 4 {
		t.Fatal(again)
	}
}

func TestIncompleteAndGaps(t *testing.T) {
	s, _ := store(t)
	id := rand.Text()
	if err := s.Observe(event(id, 2, lifecycle.TaskStarted)); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.List()
	if !runs[0].Incomplete || !runs[0].Partial || runs[0].StartedAt != nil || runs[0].FinishedAt != nil {
		t.Fatal(runs)
	}
	finish := event(id, 4, lifecycle.RunFinished)
	finish.Outcome = lifecycle.Success
	if err := s.Observe(finish); err != nil {
		t.Fatal(err)
	}
	runs, _ = s.List()
	if runs[0].Incomplete || !runs[0].Partial || runs[0].Outcome != lifecycle.Success {
		t.Fatal(runs)
	}
}

func TestRetentionAndBoundedLargeRuns(t *testing.T) {
	s, dir := store(t)
	var first string
	for i := range MaxRuns + 20 {
		id := rand.Text()
		if i == 0 {
			first = id
		}
		e := event(id, 1, lifecycle.RunStarted)
		e.Workflow = strings.Repeat("w", MaxLabelBytes)
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := s.List()
	if len(runs) != MaxRuns {
		t.Fatal(len(runs))
	}
	for _, run := range runs {
		if run.RunID == first || !run.Incomplete {
			t.Fatal(run)
		}
	}
	for i := uint64(2); i <= 1000; i++ {
		e := event(runs[0].RunID, i, lifecycle.TaskFinished)
		e.Workflow, e.Outcome = runs[0].Workflow, lifecycle.Success
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "runs.json"))
	if err != nil || info.Size() > MaxBytes/2 {
		t.Fatalf("%v %v", info, err)
	}
	runs, _ = s.List()
	if runs[0].Tasks.Success != 999 {
		t.Fatal(runs[0])
	}
	e := event(rand.Text(), 1, lifecycle.RunStarted)
	e.Workflow = strings.Repeat("x", MaxLabelBytes+1)
	if !errors.Is(s.Observe(e), ErrUnavailable) {
		t.Fatal("oversized alias accepted")
	}
}

func TestPrivatePathsAndUnsafeFiles(t *testing.T) {
	for _, name := range []string{"history.lock", "runs.json", "runs.pending"} {
		for _, kind := range []string{"symlink", "hardlink", "mode", "fifo"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				s, dir := store(t)
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("private-marker"), 0600); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, name)
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(target, path)
				case "hardlink":
					err = os.Link(target, path)
				case "mode":
					err = os.WriteFile(path, nil, 0644)
				case "fifo":
					err = syscall.Mkfifo(path, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !errors.Is(s.Observe(event(rand.Text(), 1, lifecycle.RunStarted)), ErrUnavailable) {
					t.Fatal("unsafe file accepted")
				}
				raw, _ := os.ReadFile(target)
				if string(raw) != "private-marker" {
					t.Fatal("target modified")
				}
			})
		}
	}
	for _, component := range []string{"cairn", "cairn/history"} {
		t.Run(component, func(t *testing.T) {
			state := t.TempDir()
			if err := os.MkdirAll(filepath.Join(state, filepath.Dir(component)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(state, component)); err != nil {
				t.Fatal(err)
			}
			if _, err := open(state); err != ErrUnavailable {
				t.Fatal(err)
			}
		})
	}
	s, dir := store(t)
	if err := s.Observe(event(rand.Text(), 1, lifecycle.RunStarted)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, filepath.Join(dir, "history.lock"), filepath.Join(dir, "runs.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s: %v", path, info.Mode())
		}
	}
}

func TestEnvironment(t *testing.T) {
	home, state := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", state)
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(state, "cairn/history")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	s, err = Open()
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(home, ".local/state/cairn/history")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	if _, err := Open(); err != ErrUnavailable {
		t.Fatal(err)
	}
}

func TestCorruptAndOversizedSnapshot(t *testing.T) {
	for _, raw := range []string{"{", `{"version":2,"runs":[]}`, `{"version":1}`, `{"version":1,"runs":null}`,
		`{"version":1,"runs":[],"secret":"never-load"}`, strings.Repeat(" ", MaxBytes+1),
		`{"version":1,"runs":[{"run_id":"x","config_id":"/private/config"}]}`} {
		s, dir := store(t)
		if err := os.WriteFile(filepath.Join(dir, "runs.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.List(); err != ErrUnavailable {
			t.Fatal(err)
		}
		if err := s.Observe(event(rand.Text(), 1, lifecycle.RunStarted)); err != ErrUnavailable {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
		if string(after) != raw {
			t.Fatal("corruption overwritten")
		}
	}
}

func TestLockBoundedWait(t *testing.T) {
	s, dir := store(t)
	f, err := os.OpenFile(filepath.Join(dir, "history.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Observe(event(rand.Text(), 1, lifecycle.RunStarted)); err != ErrUnavailable {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < lockWait || elapsed > 2*time.Second {
		t.Fatal(elapsed)
	}
	done := make(chan error, 1)
	go func() { done <- s.Observe(event(rand.Text(), 1, lifecycle.RunStarted)) }()
	time.Sleep(20 * time.Millisecond)
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentProcesses(t *testing.T) {
	if state := os.Getenv("CAIRN_HISTORY_TEST_STATE"); state != "" {
		s, err := open(state)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		var acknowledged []lifecycle.Event
		for range 5 {
			id := rand.Text()
			for seq := uint64(1); seq <= 3; seq++ {
				e := event(id, seq, lifecycle.TaskFinished)
				e.Outcome = lifecycle.Success
				if err := s.Observe(e); err == nil {
					acknowledged = append(acknowledged, e)
				} else if err != ErrUnavailable {
					t.Fatal(err)
				}
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(acknowledged); err != nil {
			t.Fatal(err)
		}
		return
	}
	s, dir := store(t)
	state := filepath.Dir(filepath.Dir(dir))
	var wg sync.WaitGroup
	var mu sync.Mutex
	expected := make(map[string]Summary)
	for range 3 {
		wg.Go(func() {
			cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcesses$")
			cmd.Env = append(os.Environ(), "CAIRN_HISTORY_TEST_STATE="+state)
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%v: %s", err, raw)
				return
			}
			var acknowledged []lifecycle.Event
			if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&acknowledged); err != nil {
				t.Errorf("%v: %s", err, raw)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range acknowledged {
				run := expected[e.RunID]
				run.Partial = run.Partial || e.Sequence != run.Sequence+1
				run.Sequence = e.Sequence
				run.Tasks.Success++
				expected[e.RunID] = run
			}
		})
	}
	wg.Wait()
	runs, err := s.List()
	if err != nil || len(expected) == 0 || len(runs) != len(expected) {
		t.Fatalf("%d %v", len(runs), err)
	}
	for _, run := range runs {
		want := expected[run.RunID]
		if run.Tasks.Success != want.Tasks.Success || run.Sequence != want.Sequence || run.Partial != want.Partial {
			t.Fatal(run)
		}
	}
}

func TestAllowlistedPersistenceAndRecovery(t *testing.T) {
	s, dir := store(t)
	e := event(rand.Text(), 1, lifecycle.RunStarted)
	e.Operation = "backup"
	e.Repository = "/private/repository-password"
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "repository") {
		t.Fatal(string(raw))
	}
	var data snapshot
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	// A crash scratch file is reused, not accumulated or promoted on open.
	if err := os.WriteFile(filepath.Join(dir, "runs.pending"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := open(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Sequence, e.Kind, e.Outcome = 2, lifecycle.RunFinished, lifecycle.Success
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
