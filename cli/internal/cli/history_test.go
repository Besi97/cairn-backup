package cli

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

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
