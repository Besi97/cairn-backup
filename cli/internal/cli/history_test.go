package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/creack/pty"
)

type historyTerminal struct {
	mu      sync.Mutex
	file    *os.File
	output  bytes.Buffer
	warning chan struct{}
}

func (w *historyTerminal) Fd() uintptr { return w.file.Fd() }

func (w *historyTerminal) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(p)
	if bytes.Contains(p, []byte("Warning: local run history")) {
		select {
		case w.warning <- struct{}{}:
		default:
		}
	}
	return n, err
}

func (w *historyTerminal) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.output.String()
}

func TestHistoryWarningClearsAndRedrawsActiveBoard(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Setenv("TERM", "xterm-256color")
	release := filepath.Join(t.TempDir(), "release")
	t.Setenv("FAKE_RELEASE", release)
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	stderr := &historyTerminal{file: slave, warning: make(chan struct{}, 1)}
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    stages:
      - tasks:
          - {type: compact, repository: local}
          - {type: compact, repository: local}
`)
	started, finished := 0, 0
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	ctx = lifecycle.WithObserver(ctx, func(e lifecycle.Event) error {
		if e.Kind == lifecycle.TaskStarted {
			started++
			if started == 2 {
				// The first task stays on the board until the fixture is released.
				pending := filepath.Join(os.Getenv("XDG_STATE_HOME"), "cairn/history/runs.pending")
				if err := os.WriteFile(pending, nil, 0644); err != nil {
					t.Error(err)
				}
			}
		}
		if e.Kind == lifecycle.TaskFinished {
			finished++
		}
		return nil
	})
	var stdout bytes.Buffer
	done := make(chan int, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &stdout, stderr, "test")
	}()
	defer func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("workflow did not stop")
		}
	}()
	select {
	case <-stderr.warning:
	case <-ctx.Done():
		t.Fatalf("missing history warning: %q", stderr.text())
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.text())
		}
	case <-ctx.Done():
		t.Fatal("workflow did not finish")
	}
	text := stderr.text()
	warning := strings.Index(text, "Warning: local run history")
	if warning < 0 || !strings.HasSuffix(text[:warning], "\r\x1b[J") ||
		!strings.Contains(text[warning:], "\ncairn [manual ") {
		t.Fatalf("warning bypassed board clearing/redrawing: %q", text)
	}
	if strings.Count(text, "Warning: local run history") != 1 || strings.Contains(stdout.String(), "history") ||
		started != 2 || finished != 2 || !strings.Contains(stdout.String(), "2 succeeded") {
		t.Fatalf("started=%d finished=%d stdout=%q stderr=%q", started, finished, &stdout, text)
	}
}

func TestCLIHistoryPreservesCallerAndOutcomes(t *testing.T) {
	for _, tt := range []struct {
		exit    string
		code    int
		outcome lifecycle.Outcome
	}{
		{"0", 0, lifecycle.Success}, {"1", 1, lifecycle.Warning}, {"2", 2, lifecycle.Failure},
	} {
		t.Run(tt.exit, func(t *testing.T) {
			fakeBorgEnvironment(t)
			t.Setenv("FAKE_EXIT", tt.exit)
			count := 0
			ctx := lifecycle.WithObserver(t.Context(), func(lifecycle.Event) error {
				count++
				return errors.New("private-caller-error")
			})
			var output bytes.Buffer
			if code := Run(ctx, backupArgs(), &output, &output, "test"); code != tt.code {
				t.Fatalf("code=%d: %s", code, &output)
			}
			store, err := history.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runs, err := store.List()
			if err != nil || len(runs) != 1 {
				t.Fatalf("%+v %v", runs, err)
			}
			if count != 4 || runs[0].Outcome != tt.outcome || runs[0].Incomplete || runs[0].Partial ||
				runs[0].Operation != "backup" || runs[0].ConfigID != "" || strings.Contains(output.String(), "private-caller-error") {
				t.Fatalf("count=%d runs=%+v output=%s", count, runs, &output)
			}
		})
	}
}

func TestShellWorkflowPersistsLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name, command string
		code          int
		outcome       lifecycle.Outcome
		counts        history.TaskCounts
	}{
		{"success", "printf private-shell-output", 0, lifecycle.Success, history.TaskCounts{Success: 2}},
		{"failure", "printf private-shell-output; exit 7", 2, lifecycle.Failure, history.TaskCounts{Failure: 1, Skipped: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
			path := writeWorkflowConfig(t, `version: 1
repositories: {}
workflows:
  manual:
    stages:
      - tasks:
          - {type: shell, shell: /bin/sh, command: "`+tt.command+`"}
      - tasks:
          - {type: shell, shell: /bin/sh, command: "true"}
`)
			var events []lifecycle.Event
			ctx := lifecycle.WithObserver(t.Context(), func(e lifecycle.Event) error {
				events = append(events, e)
				return nil
			})
			var output bytes.Buffer
			if code := Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &output, &output, "test"); code != tt.code {
				t.Fatalf("code=%d output=%s", code, &output)
			}
			var finished, skipped int
			for _, e := range events {
				if e.Kind == lifecycle.TaskFinished || e.Kind == lifecycle.TaskSkipped {
					if e.Operation != "shell" || e.Repository != "" {
						t.Fatalf("shell attribution: %+v", e)
					}
					if e.Kind == lifecycle.TaskFinished {
						finished++
					} else if e.Reason == lifecycle.PriorFailure {
						skipped++
					}
				}
			}
			if finished != int(tt.counts.Success+tt.counts.Failure) || skipped != int(tt.counts.Skipped) {
				t.Fatalf("events=%+v", events)
			}
			store, err := history.OpenReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			runs, err := store.List()
			if err != nil || len(runs) != 1 || runs[0].Workflow != "manual" || runs[0].Outcome != tt.outcome ||
				runs[0].Tasks != tt.counts || runs[0].Incomplete || runs[0].Partial {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
			data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "cairn/history/runs.json"))
			if err != nil || bytes.Contains(data, []byte("private-shell-output")) {
				t.Fatalf("history exposed shell text or output: %v", err)
			}
		})
	}
}

func TestCLIHistoryFailureDoesNotChangeWorkflow(t *testing.T) {
	for _, exit := range []string{"0", "1", "2"} {
		t.Run(exit, func(t *testing.T) {
			fakeBorgEnvironment(t)
			t.Setenv("FAKE_EXIT", exit)
			state := os.Getenv("XDG_STATE_HOME")
			if err := os.WriteFile(filepath.Join(state, "cairn"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			path := writeWorkflowConfig(t, workflowConfig)
			var output bytes.Buffer
			args := []string{"workflow", "run", "manual", "--config", path}
			code := Run(t.Context(), args, &output, &output, "test")
			if code != int(exit[0]-'0') || strings.Count(output.String(), "Warning: local run history unavailable") != 1 ||
				!strings.Contains(output.String(), "Warning: lifecycle") || strings.Contains(output.String(), state) {
				t.Fatalf("code=%d output=%s", code, &output)
			}
		})
	}
}

func TestCLIHistoryConfiguredIdentityAndSecrets(t *testing.T) {
	fakeBorgEnvironment(t)
	definition := strings.ReplaceAll(workflowConfig, "./repo", "/private-history-repository")
	definition = strings.ReplaceAll(definition, "docs-current", "private-history-archive")
	definition = strings.ReplaceAll(definition, "sources: [.]", "sources: [/private-history-source]")
	definition = strings.ReplaceAll(definition, "location: /private-history-repository", "passphrase: private-history-secret, location: /private-history-repository")
	first, second := writeWorkflowConfig(t, definition), writeWorkflowConfig(t, definition)
	link := filepath.Join(t.TempDir(), "alias.yaml")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string)
	for _, path := range []string{first, second, link} {
		ctx := lifecycle.WithObserver(t.Context(), func(e lifecycle.Event) error {
			ids[path] = e.ConfigID
			return nil
		})
		var output bytes.Buffer
		if code := Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &output, &output, "test"); code != 0 {
			t.Fatalf("code=%d output=%s", code, &output)
		}
	}
	if ids[first] == "" || ids[first] != ids[link] || ids[first] == ids[second] {
		t.Fatal(ids)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "cairn/history/runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{first, second, link, "private-history", "stderr diagnostic", "command", "environment"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("persisted %q: %s", secret, raw)
		}
	}
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.List()
	if err != nil || len(runs) != 3 {
		t.Fatalf("%+v %v", runs, err)
	}
	for _, run := range runs {
		if run.Workflow != "manual" || run.ConfigID == "" || run.Tasks.Success != 3 || run.Partial || run.Incomplete {
			t.Fatal(run)
		}
	}
}

func TestCLINonRunsDoNotCreateHistory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing-state")
	t.Setenv("XDG_STATE_HOME", state)
	path := writeWorkflowConfig(t, workflowConfig)
	for _, args := range [][]string{
		nil, {"--help"}, {"--version"}, {"backup"}, {"check", "--help"}, {"tui"},
		{"workflow", "list", "--config", path}, {"config", "check", "--config", path},
	} {
		var output bytes.Buffer
		Run(t.Context(), args, &output, &output, "test")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("non-run created state: %v", err)
	}
}

func TestHistoryRecorderIsLazyAndWarningsAreLimited(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	warnings := 0
	recorder := &historyRecorder{warn: func() { warnings++ }}
	recorder.close()
	if _, err := os.Stat(filepath.Join(state, "cairn")); !os.IsNotExist(err) {
		t.Fatalf("unused history created state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(state, "cairn"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(e lifecycle.Event) error {
		events = append(events, e)
		return recorder.observe(e)
	})
	run := lifecycle.New(ctx, lifecycle.Event{Operation: "backup"})
	run.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
	run.Emit(lifecycle.Event{Kind: lifecycle.TaskFinished, Outcome: lifecycle.Success})
	if warnings != 1 || lifecycle.Err(ctx) != lifecycle.ErrDelivery || len(events) != 2 {
		t.Fatalf("warnings=%d err=%v events=%v", warnings, lifecycle.Err(ctx), events)
	}
	// A later new event can still persist; failed events are never replayed.
	if err := os.Remove(filepath.Join(state, "cairn")); err != nil {
		t.Fatal(err)
	}
	run.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
	recorder.close()
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.List()
	if err != nil || len(runs) != 1 || !runs[0].Partial || runs[0].Incomplete || runs[0].Tasks.Success != 0 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestHistoryRecorderSafeFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", "")
	warnings := 0
	recorder := &historyRecorder{warn: func() { warnings++ }}
	defer recorder.close()
	err := recorder.observe(lifecycle.Event{RunID: rand.Text(), Kind: lifecycle.RunStarted})
	if err != history.ErrUnavailable || warnings != 1 {
		t.Fatalf("err=%v warnings=%d", err, warnings)
	}
}
