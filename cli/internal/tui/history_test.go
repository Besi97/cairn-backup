package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/report"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/charmbracelet/x/ansi"
)

func TestHistoryRecoveryDetailsEscape(t *testing.T) {
	n := report.Step{Status: report.NotRun}
	e := report.Evidence{Outcome: n, Selection: n, IdentityBefore: n, Extraction: n, IdentityAfter: n, ChecksState: n, Cleanup: n,
		Sample: report.Sample{Status: "not_run"}, Archive: &report.Archive{Name: "archive\x1b"}, Checks: []report.Check{{Kind: "path_type", Path: "file\n", Status: "passed"}}}
	h, err := report.ForHistory(e)
	if err != nil {
		t.Fatal(err)
	}
	m := historyModel(t)
	m.history.runs = []history.Observation{{Summary: history.Summary{Recovery: []history.RecoveryTask{{Stage: 1, Task: 2, Evidence: h}}}}}
	text := ansi.Strip(m.runDetail())
	if !strings.Contains(text, `archive\x1b`) || !strings.Contains(text, `file\n`) || strings.ContainsRune(text, '\x1b') {
		t.Fatalf("unsafe details: %q", text)
	}
}

func historyModel(t *testing.T) Model {
	t.Helper()
	m := sizedModel()
	m.history.ctx = t.Context()
	t.Cleanup(m.history.close)
	m.page = archivesPage
	return m
}

func finishHistory(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	m, next := nextScan(t, m, cmd)
	if next != nil || m.history.loading {
		t.Fatal("history did not complete in one bounded update")
	}
	<-m.history.job.done
	return m
}

func TestHistoryMissingAndRefreshReopens(t *testing.T) {
	state := filepath.Join(t.TempDir(), "absent")
	t.Setenv("XDG_STATE_HOME", state)
	m := historyModel(t)
	m, cmd := press(m, tea.KeyTab, 0)
	if m.page != historyPage || cmd == nil {
		t.Fatal("history tab did not start initial read")
	}
	m = finishHistory(t, m, cmd)
	contains(t, m, "No recorded runs.", "Snapshot inspected:")
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created storage: %v", err)
	}
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(lifecycle.Event{RunID: "new-run", Sequence: 1, Time: time.Now(),
		Kind: lifecycle.RunStarted, Source: lifecycle.Manual, Operation: "check"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	m, _ = press(m, tea.KeyTab, 0)
	m, cmd = press(m, tea.KeyTab, tea.ModShift)
	if cmd != nil || len(m.history.runs) != 0 {
		t.Fatal("returning to history refreshed automatically")
	}
	m, cmd = press(m, 'g', 0)
	m = finishHistory(t, m, cmd)
	contains(t, m, "check", "unknown · incomplete")
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != runPage || m.tab() != historyPage {
		t.Fatal("summary is not under History")
	}
	contains(t, m, "Recorded outcome: unknown", "Incomplete: true", "Owner: unknown", "Lock: unknown",
		"Started:", "Finished: unknown", "Configuration identity: unknown", "differences from TUI/agent snapshots are unknown")
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != historyPage {
		t.Fatal("Escape lost the history list")
	}
}

func TestHistorySafeErrorsAndPreviousSnapshot(t *testing.T) {
	for _, mode := range []string{"corrupt", "unreadable", "unsafe"} {
		t.Run(mode, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			directory := filepath.Join(state, "cairn/history")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "runs.json")
			if err := os.WriteFile(path, []byte("private-error\x1b[31m"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unreadable":
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
			case "unsafe":
				if err := os.Chmod(directory, 0755); err != nil {
					t.Fatal(err)
				}
			}
			m := historyModel(t)
			m, cmd := press(m, tea.KeyTab, 0)
			m = finishHistory(t, m, cmd)
			contains(t, m, "Local run history unavailable.")
			text := modelText(m)
			for _, forbidden := range []string{"private-error", state, "No recorded runs."} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("unsafe error display: %q", text)
				}
			}
			m.history.runs = []history.Observation{{Summary: history.Summary{RunID: "previous"}}}
			m.history.at = time.Now().Add(-time.Hour)
			m, cmd = press(m, 'g', 0)
			m = finishHistory(t, m, cmd)
			contains(t, m, "Previous snapshot retained.", "previous")
		})
	}
}

func TestHistoryReadDoesNotMutateOrAcquireLocks(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_RUNTIME_DIR", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()
	lock, err := locks.Workflow(identity, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	evidence := runlock.Capture(lock)
	if evidence == nil {
		t.Fatal("missing execution evidence")
	}
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	for i := range history.MaxRuns + 5 {
		if err := store.Observe(lifecycle.Event{RunID: fmt.Sprintf("run-%03d", i), Sequence: 1,
			Time: time.Unix(int64(i+1), 0), Source: lifecycle.Manual, Workflow: "nightly",
			ConfigID: identity.Digest(), Kind: lifecycle.RunStarted, Execution: evidence}); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	directory := filepath.Join(state, "cairn/history")
	snapshot := filepath.Join(directory, "runs.json")
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := os.OpenFile(filepath.Join(directory, "history.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer persistence.Close()
	if err := syscall.Flock(int(persistence.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	m := historyModel(t)
	m, cmd := press(m, tea.KeyTab, 0)
	m = finishHistory(t, m, cmd)
	if len(m.history.runs) != history.MaxRuns || m.history.runs[0].Summary.RunID != "run-104" ||
		m.history.runs[history.MaxRuns-1].Summary.RunID != "run-005" {
		t.Fatal("history did not retain backend ordering and bound")
	}
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Owner: present", "Lock: held", "Recorded outcome: unknown", "Incomplete: true")
	for range 3 {
		m, cmd = press(m, 'g', 0)
		m = finishHistory(t, m, cmd)
	}
	m, _ = press(m, tea.KeyEscape, 0)
	for range history.MaxRuns {
		m, _ = press(m, 'j', 0)
	}
	if m.history.cursor != history.MaxRuns-1 || !strings.Contains(ansi.Strip(m.body.View()), "> nightly") {
		t.Fatal("large history list selection is not visible")
	}
	m, _ = press(m, 'q', 0)
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("history changed: %v", err)
	}
	afterInfo, err := os.Stat(snapshot)
	if err != nil || !afterInfo.ModTime().Equal(info.ModTime()) || !os.SameFile(info, afterInfo) {
		t.Fatalf("snapshot rewritten: %v", err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 2 {
		t.Fatalf("inspection created files: %v %v", files, err)
	}
	if _, err := locks.Workflow(identity, "nightly"); !errors.Is(err, runlock.ErrBusy) {
		t.Fatalf("TUI exit released unrelated execution lock: %v", err)
	}
}

func TestHistorySummaryFieldsAndEscaping(t *testing.T) {
	m := historyModel(t)
	now := time.Date(2026, 11, 17, 12, 30, 0, 0, time.UTC)
	m.history.at = now
	m.page = runPage
	for _, outcome := range []lifecycle.Outcome{lifecycle.Success, lifecycle.Warning, lifecycle.Failure, lifecycle.Interrupted, lifecycle.Skipped, ""} {
		m.history.runs = []history.Observation{{
			Summary: history.Summary{
				RunID: "run\x1b[31m\nid", Workflow: "nightly\x1b]0;bad\a", ConfigID: strings.Repeat("a", 64),
				Source: lifecycle.Scheduled, ScheduledAt: now, Preview: true,
				FirstObserved: now, LastObserved: now, StartedAt: &now, FinishedAt: &now, Sequence: 500,
				Outcome: outcome, Reason: lifecycle.PriorWarning, Partial: true,
				Tasks: history.TaskCounts{Success: 10, Warning: 2, Failure: 3, Interrupted: 4, Skipped: 5},
			},
			Observation: runlock.Observation{Owner: runlock.OwnerGone, Lock: runlock.LockUnknown},
		}}
		m.refresh()
		want := string(outcome)
		if want == "" {
			want = "unknown"
		}
		contains(t, m, "Recorded outcome: "+want, "Reason: prior_stage_warning", "Partial: true",
			"Success: 10 · warning: 2 · failure: 3", "Interrupted: 4 · skipped: 5",
			"Owner: gone", "Lock: unknown", "gone does not mean Borg exited", "not a content hash",
			"Partial counts may be incomplete", "Source: scheduled", "Preview: true", "Last sequence: 500")
		if strings.ContainsAny(ansi.Strip(m.body.GetContent()), "\x1b\a") {
			t.Fatal("terminal controls were not escaped")
		}
	}
	m.history.runs[0].Summary.Incomplete = true
	m.history.runs[0].Summary.Outcome = lifecycle.Success
	m.refresh()
	contains(t, m, "Recorded outcome: unknown", "Incomplete: true")
}

func TestHistoryRefreshSelectionAndEviction(t *testing.T) {
	m := historyModel(t)
	runs := []history.Observation{{Summary: history.Summary{RunID: "first"}}, {Summary: history.Summary{RunID: "selected"}}}
	m.history.source = func(context.Context) ([]history.Observation, error) { return runs, nil }
	m, cmd := press(m, tea.KeyTab, 0)
	m = finishHistory(t, m, cmd)
	m, _ = press(m, 'j', 0)
	m, _ = press(m, tea.KeyEnter, 0)
	runs = []history.Observation{runs[1], runs[0]}
	old := m.history.job
	m, cmd = press(m, 'g', 0)
	m = finishHistory(t, m, cmd)
	if m.page != runPage || m.history.cursor != 0 {
		t.Fatal("refresh lost selected run identity")
	}
	next, staleCmd := m.Update(historyUpdate{job: old, runs: nil})
	m = next.(Model)
	if staleCmd != nil || len(m.history.runs) != 2 {
		t.Fatal("stale read replaced refreshed results")
	}
	runs = runs[1:]
	m, cmd = press(m, 'g', 0)
	m, _ = press(m, '?', 0)
	m = finishHistory(t, m, cmd)
	m, _ = press(m, '?', 0)
	if m.page != historyPage {
		t.Fatal("eviction showed another run as selected detail")
	}
	m, cmd = press(m, 'r', 0)
	if !m.reloading || cmd == nil || m.history.loading {
		t.Fatal("r no longer reloads configuration independently")
	}
}

func TestHistoryCancellationAndBoundedRefresh(t *testing.T) {
	for _, exit := range []string{"escape", "tab", "quit", "interrupt", "context"} {
		t.Run(exit, func(t *testing.T) {
			m := historyModel(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m.history.ctx = ctx
			started, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var calls atomic.Int32
			m.history.source = func(ctx context.Context) ([]history.Observation, error) {
				calls.Add(1)
				close(started)
				<-ctx.Done()
				<-release
				return nil, ctx.Err()
			}
			m, cmd := press(m, tea.KeyTab, 0)
			<-started
			for range 100 {
				var repeated tea.Cmd
				m, repeated = press(m, 'g', 0)
				if repeated != nil {
					t.Fatal("refresh started overlapping reads")
				}
			}
			old := m.history.job
			switch exit {
			case "escape":
				m, _ = press(m, tea.KeyEscape, 0)
			case "tab":
				m, _ = press(m, tea.KeyTab, 0)
			case "quit":
				m, _ = press(m, 'q', 0)
			case "interrupt":
				m, _ = press(m, 'c', tea.ModCtrl)
			case "context":
				cancel()
				m.history.stop()
			}
			if m.history.loading {
				t.Fatal("leaving history did not cancel the read")
			}
			m.page = historyPage
			m, repeated := press(m, 'g', 0)
			if repeated != nil || calls.Load() != 1 {
				t.Fatal("refresh overlapped cancelled read cleanup")
			}
			updated, _ := m.Update(historyUpdate{job: old, runs: []history.Observation{{Summary: history.Summary{RunID: "stale"}}}})
			m = updated.(Model)
			if len(m.history.runs) != 0 {
				t.Fatal("cancelled update was applied")
			}
			m, _ = press(m, '?', 0)
			m, _ = press(m, tea.KeyPgDown, 0)
			close(release)
			<-old.done
			if msg := cmd(); msg != nil {
				t.Fatalf("cancelled read published a result: %v", msg)
			}
		})
	}
}
