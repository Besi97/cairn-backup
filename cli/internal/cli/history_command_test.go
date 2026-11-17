package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestHistoryCommandEmptyAndArguments(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(t.TempDir(), "missing-runtime"))
	t.Setenv("PATH", "")
	for _, tt := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"history"}, 0, "No recorded runs.\n"},
		{[]string{"history", "--incomplete"}, 0, "No incomplete recorded runs.\n"},
		{[]string{"history", "--help"}, 0, "Incomplete means"},
		{[]string{"history", "unexpected"}, 3, ""},
		{[]string{"history", "--incomplete=invalid"}, 3, ""},
		{[]string{"history", "--unknown"}, 3, ""},
	} {
		var out, diagnostics bytes.Buffer
		count := 0
		ctx := lifecycle.WithObserver(t.Context(), func(lifecycle.Event) error { count++; return nil })
		code := Run(ctx, tt.args, &out, &diagnostics, "test")
		if code != tt.code || !strings.Contains(out.String(), tt.text) || count != 0 {
			t.Fatalf("%v: code=%d events=%d out=%q err=%q", tt.args, code, count, &out, &diagnostics)
		}
		if code == 0 && diagnostics.Len() != 0 {
			t.Fatal(diagnostics.String())
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("inspection created state: %v", err)
	}
	if _, err := os.Stat(os.Getenv("XDG_RUNTIME_DIR")); !os.IsNotExist(err) {
		t.Fatalf("inspection created runtime state: %v", err)
	}
}

func TestHistoryCommandRecordedOutcomes(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("PATH", "")
	s, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	for i, outcome := range []lifecycle.Outcome{
		lifecycle.Success, lifecycle.Warning, lifecycle.Failure, lifecycle.Interrupted, lifecycle.Skipped, "",
	} {
		e := lifecycle.Event{
			RunID: "run-" + string(rune('a'+i)), Sequence: 2, Time: at.Add(time.Duration(i) * time.Second),
			Source: lifecycle.Scheduled, Workflow: "daily\n\x1b[2J", ConfigID: strings.Repeat("a", 64),
			ScheduledAt: at.Add(-time.Hour), Preview: true, Kind: lifecycle.TaskFinished, Outcome: lifecycle.Warning,
		}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
		if outcome != "" {
			e.Sequence, e.Kind, e.Outcome, e.Reason = 3, lifecycle.RunFinished, outcome, lifecycle.PriorWarning
			if err := s.Observe(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	standalone := lifecycle.Event{
		RunID: "standalone", Sequence: 1, Time: at.Add(time.Hour),
		Source: lifecycle.Manual, Operation: "backup", Kind: lifecycle.RunStarted,
	}
	if err := s.Observe(standalone); err != nil {
		t.Fatal(err)
	}
	standalone.Sequence, standalone.Kind, standalone.Outcome = 2, lifecycle.RunFinished, lifecycle.Success
	if err := s.Observe(standalone); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "cairn", "history", "runs.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if code := Run(t.Context(), []string{"history"}, &out, &diagnostics, "test"); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("code=%d err=%s", code, &diagnostics)
	}
	text := out.String()
	for _, want := range []string{
		`workflow="daily\n\x1b[2J"`, "source=scheduled preview=true config=" + strings.Repeat("a", 64),
		"outcome=unknown incomplete=true partial=true", "owner=unknown lock=unknown",
		"outcome=success incomplete=false partial=true", "outcome=warning", "outcome=failure",
		"outcome=interrupted", "outcome=skipped", `reason="prior_stage_warning"`,
		"started=unknown", "finished=unknown", "due=2026-11-17T11:00:00Z",
		"tasks: success=0 warning=1 failure=0 interrupted=0 skipped=0",
		`operation="backup" source=manual preview=false config=unknown`,
		"outcome=success incomplete=false partial=false",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if strings.Index(text, `run "run-f"`) > strings.Index(text, `run "run-a"`) ||
		strings.ContainsRune(text, '\x1b') || strings.Contains(text, state) {
		t.Fatalf("unsafe or unordered output: %s", text)
	}
	out.Reset()
	if code := Run(t.Context(), []string{"history", "--incomplete"}, &out, &diagnostics, "test"); code != 0 ||
		!strings.Contains(out.String(), `run "run-f"`) || strings.Count(out.String(), "\nrun ") != 1 {
		t.Fatalf("code=%d out=%s err=%s", code, &out, &diagnostics)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("command mutated history")
	}
}

func TestHistoryCommandSafeUnavailable(t *testing.T) {
	for _, mode := range []string{"malformed", "permission", "unsafe-directory"} {
		t.Run(mode, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			dir := filepath.Join(state, "cairn", "history")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "runs.json")
			perm := os.FileMode(0600)
			if mode == "permission" {
				perm = 0000
			}
			if err := os.WriteFile(path, []byte("private-secret-payload"), perm); err != nil {
				t.Fatal(err)
			}
			if mode == "unsafe-directory" {
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			var out, diagnostics bytes.Buffer
			if code := Run(t.Context(), []string{"history"}, &out, &diagnostics, "test"); code != 2 ||
				out.Len() != 0 || diagnostics.String() != "Error: local run history unavailable\n" {
				t.Fatalf("code=%d out=%q err=%q", code, &out, &diagnostics)
			}
			out.Reset()
			diagnostics.Reset()
			if code := Run(t.Context(), []string{"history", "--help"}, &out, &diagnostics, "test"); code != 0 || diagnostics.Len() != 0 {
				t.Fatalf("help read invalid history: code=%d err=%q", code, &diagnostics)
			}
		})
	}
}

func TestHistoryOutputDoesNotInferLiveness(t *testing.T) {
	for _, owner := range []runlock.OwnerState{runlock.OwnerPresent, runlock.OwnerGone, runlock.OwnerUnknown} {
		for _, lock := range []runlock.LockState{runlock.LockHeld, runlock.LockUnknown} {
			var out bytes.Buffer
			err := writeHistory(&out, []history.Observation{{
				Summary:     history.Summary{RunID: "uncertain", Incomplete: true},
				Observation: runlock.Observation{Owner: owner, Lock: lock},
			}}, false)
			if err != nil || !strings.Contains(out.String(), "outcome=unknown incomplete=true") ||
				!strings.Contains(out.String(), "owner="+string(owner)+" lock="+string(lock)) {
				t.Fatalf("%s %s: %s %v", owner, lock, &out, err)
			}
		}
	}
	var out bytes.Buffer
	if err := writeHistory(&out, []history.Observation{{Summary: history.Summary{RunID: "terminal"}}}, true); err != nil ||
		out.String() != "No incomplete recorded runs.\n" {
		t.Fatalf("empty filtered output=%q err=%v", &out, err)
	}
}

type failedHistoryOutput struct{}

func (failedHistoryOutput) Write([]byte) (int, error) {
	return 0, errors.New("private-output-path")
}

func TestHistoryCommandOutputFailureAndCancellation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "missing"))
	var diagnostics bytes.Buffer
	if code := Run(t.Context(), []string{"history"}, failedHistoryOutput{}, &diagnostics, "test"); code != 2 ||
		diagnostics.String() != "Error: write local run history output failed\n" {
		t.Fatalf("code=%d err=%q", code, &diagnostics)
	}
	diagnostics.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := Run(ctx, []string{"history"}, &diagnostics, &diagnostics, "test"); code != 143 {
		t.Fatalf("cancelled code=%d", code)
	}
}

func TestHistoryCommandIndependentOwnerAndLockEvidence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	config := filepath.Join(t.TempDir(), "private-config-path")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := runlock.Config(config)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()
	lock, err := locks.Workflow(identity, "daily")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	evidence := runlock.Capture(lock)
	if evidence == nil {
		t.Fatal("missing execution evidence")
	}
	s, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, name := range []string{"present", "stale", "missing"} {
		e := lifecycle.Event{
			RunID: name, Workflow: "daily", ConfigID: identity.Digest(),
			Sequence: 1, Source: lifecycle.Manual, Time: time.Now().UTC().Add(time.Duration(i) * time.Second),
			Kind: lifecycle.RunStarted,
		}
		copy := *evidence
		e.Execution = &copy
		if name == "stale" {
			copy.StartTicks++
		} else if name == "missing" {
			e.Execution = nil
		}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
	}
	var out, diagnostics bytes.Buffer
	if code := Run(t.Context(), []string{"history", "--incomplete"}, &out, &diagnostics, "test"); code != 0 || diagnostics.Len() != 0 {
		t.Fatalf("code=%d err=%q", code, &diagnostics)
	}
	sections := strings.Split(out.String(), "\nrun ")
	for i, want := range []string{"owner=unknown lock=unknown", "owner=gone lock=held", "owner=present lock=held"} {
		if len(sections) != 4 || !strings.Contains(sections[i+1], want) ||
			!strings.Contains(sections[i+1], "outcome=unknown incomplete=true") {
			t.Fatalf("missing independent evidence %s: %s", want, &out)
		}
	}
	for _, private := range []string{config, evidence.LockKey, evidence.BootID, os.Getenv("XDG_RUNTIME_DIR")} {
		if strings.Contains(out.String(), private) {
			t.Fatalf("exposed execution detail %q", private)
		}
	}
	// Inspection must not release the execution lock or make it available.
	other, err := locks.Workflow(identity, "daily")
	if other != nil {
		other.Close()
		t.Fatal("inspection released execution lock")
	}
	if err == nil {
		t.Fatal("expected busy lock")
	}
}
