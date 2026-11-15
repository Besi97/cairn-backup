package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestPreCanceledLockedWorkflow(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// No usable environment or identity: cancellation must win before IO.
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	result, err := (Executor{}).RunLocked(ctx, runlock.Identity{}, Plan{})
	if !errors.Is(err, context.Canceled) || result.Outcome != borg.Interrupted {
		t.Fatalf("pre-canceled: %+v, %v", result, err)
	}
}

func TestLockedWorkflowRequiresInheritance(t *testing.T) {
	base := t.TempDir()
	runtime := filepath.Join(base, "runtime")
	if err := os.Mkdir(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path := filepath.Join(base, "config")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	executor := Executor{Runner: runnerFunc(func(context.Context, borg.Request) (borg.Result, error) {
		t.Fatal("custom runner executed without inheriting locks")
		return borg.Result{}, nil
	})}
	result, err := executor.RunLocked(t.Context(), id, Plan{Name: "nightly"})
	if err == nil || result.Outcome != borg.Failure {
		t.Fatalf("custom runner: %+v, %v", result, err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(id, "nightly")
	if err != nil {
		t.Fatal("setup failure leaked lock:", err)
	}
	lock.Close()
}
