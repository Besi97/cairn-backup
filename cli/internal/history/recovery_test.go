package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
)

func recoveryReport() *report.Evidence {
	step := report.Step{Status: report.Passed}
	return &report.Evidence{
		Outcome: step, Selection: step, IdentityBefore: step, Extraction: step,
		IdentityAfter: step, ChecksState: step, Cleanup: step,
		Sample:  report.Sample{Status: "not_run"},
		Archive: &report.Archive{Name: "archive"},
		Scope:   report.Scope{Paths: []string{"path"}},
		Checks:  []report.Check{{Path: "check-path", Kind: "path_type", Status: "passed"}},
	}
}

func TestRecoverySnapshotOverflowPreservesSummary(t *testing.T) {
	s, dir := store(t)
	h, err := report.ForHistory(*recoveryReport())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	data := snapshot{Version: 1}
	for i := 0; i < MaxRuns; i++ {
		data.Runs = append(data.Runs, Summary{RunID: fmt.Sprintf("run-%03d", i), Source: lifecycle.Manual,
			FirstObserved: now, LastObserved: now, Sequence: 1, Incomplete: true})
	}
	full := false
	for i := range data.Runs {
		for range MaxRecoveryTasks {
			data.Runs[i].Recovery = append(data.Runs[i].Recovery, RecoveryTask{1, 1, h})
			raw, _ := json.Marshal(data)
			if len(raw) > MaxBytes/2 {
				data.Runs[i].Recovery = data.Runs[i].Recovery[:len(data.Runs[i].Recovery)-1]
				full = true
				break
			}
		}
		if full {
			break
		}
	}
	if !full {
		t.Fatal("test did not reach snapshot limit")
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	e := event("run-099", 2, lifecycle.TaskFinished)
	e.Stage, e.Task, e.Outcome, e.Recovery = 1, 1, lifecycle.Success, recoveryReport()
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	e = event("run-099", 3, lifecycle.RunFinished)
	e.Outcome = lifecycle.Success
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil || len(raw) > MaxBytes/2 {
		t.Fatalf("snapshot size %d: %v", len(raw), err)
	}
	runs, err := s.List()
	if err != nil || len(runs) != MaxRuns {
		t.Fatalf("runs: %d, %v", len(runs), err)
	}
	var omitted uint64
	var finished bool
	for _, r := range runs {
		omitted += r.RecoveryOmitted
		for _, task := range r.Recovery {
			if task.Evidence.DetailsOmitted {
				omitted++
			}
		}
		if r.RunID == "run-099" {
			finished = !r.Incomplete && r.Outcome == lifecycle.Success
		}
	}
	if omitted == 0 || !finished {
		t.Fatalf("omitted=%d finished=%t", omitted, finished)
	}
}

func TestRecoveryPersistencePrivacyAndBounds(t *testing.T) {
	s, dir := store(t)
	for i := 1; i <= MaxRecoveryTasks+2; i++ {
		e := event("run", uint64(i), lifecycle.TaskFinished)
		e.Stage, e.Task, e.Outcome, e.Recovery = 1, i, lifecycle.Success, recoveryReport()
		if i == MaxRecoveryTasks+2 {
			e.Recovery.Outcome.Reason = "secret-diagnostic"
		}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || len(raw) > MaxBytes/2 {
		t.Fatalf("unsafe or oversized snapshot: %s", raw)
	}
	readonly, err := openStore(filepath.Dir(filepath.Dir(dir)), true)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	runs, err := readonly.List()
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	if len(runs[0].Recovery) != MaxRecoveryTasks || runs[0].RecoveryOmitted != 2 || runs[0].Tasks.Success != MaxRecoveryTasks+2 {
		t.Fatalf("%+v", runs[0])
	}
}

func TestRecoveryConcurrentWriters(t *testing.T) {
	s, dir := store(t)
	other, err := open(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	for i, writer := range []*Store{s, other} {
		wg.Add(1)
		go func(i int, writer *Store) {
			defer wg.Done()
			e := event(string(rune('a'+i)), 1, lifecycle.TaskFinished)
			e.Stage, e.Task, e.Outcome, e.Recovery = 1, 1, lifecycle.Success, recoveryReport()
			if err := writer.Observe(e); err != nil {
				t.Error(err)
			}
		}(i, writer)
	}
	wg.Wait()
	runs, err := s.List()
	if err != nil || len(runs) != 2 || len(runs[0].Recovery) != 1 || len(runs[1].Recovery) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
}

func TestRecoveryReadsExistingSnapshot(t *testing.T) {
	s, dir := store(t)
	old := `{"version":1,"runs":[{"run_id":"old","source":"manual","scheduled_at":"0001-01-01T00:00:00Z","first_observed":"2025-01-01T00:00:00Z","last_observed":"2025-01-01T00:00:00Z","sequence":1,"incomplete":true,"tasks":{"success":0,"warning":0,"failure":0,"interrupted":0,"skipped":0}}]}`
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	e := event("new", 1, lifecycle.TaskFinished)
	e.Stage, e.Task, e.Recovery = 1, 1, recoveryReport()
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	runs, err := s.List()
	if err != nil || len(runs) != 2 {
		t.Fatalf("%+v %v", runs, err)
	}
}

func TestRecoveryMalformedDetailsFailClosed(t *testing.T) {
	s, dir := store(t)
	e := event("run", 1, lifecycle.TaskFinished)
	e.Stage, e.Task, e.Recovery = 1, 1, recoveryReport()
	if err := s.Observe(e); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "runs.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data snapshot
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data.Runs[0].Recovery[0].Evidence.Details.Paths[0] = "unsafe\x1b"
	raw, _ = json.Marshal(data)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := openStore(filepath.Dir(filepath.Dir(dir)), true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.List(); err != ErrUnavailable {
		t.Fatalf("malformed details: %v", err)
	}
	if err := reader.Observe(e); err != ErrUnavailable {
		t.Fatalf("read-only write: %v", err)
	}
}
