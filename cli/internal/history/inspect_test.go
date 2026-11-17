package history

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestInspectPreservesRecordedFacts(t *testing.T) {
	s, dir := store(t)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	config := filepath.Join(t.TempDir(), "secret-config-path")
	if err := os.WriteFile(config, nil, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := runlock.Config(config)
	if err != nil {
		t.Fatal(err)
	}
	locks, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer locks.Close()
	lock, err := locks.Workflow(id, "daily")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	evidence := runlock.Capture(lock)
	if evidence == nil {
		t.Fatal("missing evidence")
	}
	for _, name := range []string{"old", "partial", "terminal"} {
		e := event(name, 2, lifecycle.TaskFinished)
		e.Workflow, e.ConfigID, e.Outcome = "daily", id.Digest(), lifecycle.Warning
		if name != "old" {
			e.Execution = evidence
		}
		if err := s.Observe(e); err != nil {
			t.Fatal(err)
		}
		if name == "terminal" {
			e.Sequence, e.Kind = 3, lifecycle.RunFinished
			if err := s.Observe(e); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := os.ReadFile(filepath.Join(dir, "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(config)) || bytes.Contains(before, []byte(os.Getenv("XDG_RUNTIME_DIR"))) {
		t.Fatal("stored a raw path")
	}
	recorded, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.Inspect()
	if err != nil || len(observed) != len(recorded) {
		t.Fatalf("%+v %v", observed, err)
	}
	for index, row := range observed {
		if !reflect.DeepEqual(row.Summary, recorded[index]) || !row.Summary.Partial || row.Summary.Tasks.Warning != 1 {
			t.Fatalf("changed recorded facts: %+v", row)
		}
		if row.Summary.RunID == "old" {
			if row.Owner != runlock.OwnerUnknown || row.Lock != runlock.LockUnknown {
				t.Fatal(row)
			}
		} else if row.Owner != runlock.OwnerPresent || row.Lock != runlock.LockHeld {
			t.Fatal(row)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "runs.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("inspection wrote history")
	}
	// Snapshot resources can be reopened after a crash/restart without changing facts.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := open(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Inspect()
	if err != nil || !reflect.DeepEqual(again, observed) {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestInvalidEvidenceAndUnavailableInspection(t *testing.T) {
	s, dir := store(t)
	e := event("invalid", 1, lifecycle.RunStarted)
	e.Execution = &runlock.Evidence{LockKey: "../secret"}
	if s.Observe(e) != ErrUnavailable {
		t.Fatal("invalid evidence accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "runs.json"), []byte(strings.Repeat("x", MaxBytes)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Inspect(); err != ErrUnavailable {
		t.Fatalf("unavailable history = %v", err)
	}
}
