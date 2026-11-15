package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestRealBorgWorkflowCrashLock(t *testing.T) {
	for _, mode := range []string{"manual", "agent"} {
		t.Run(mode, func(t *testing.T) { realBorgCrashLock(t, mode) })
	}
}

func realBorgCrashLock(t *testing.T, mode string) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	repository := filepath.Join(t.TempDir(), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	holdRepository(t, ctx, binary, repository)
	path := writeWorkflowConfig(t, fmt.Sprintf(`version: 1
repositories:
  local: {location: %q}
workflows:
  manual:
    stages:
      - tasks: [{type: compact, repository: local, lock_wait: 5}]
`, repository))
	var cmd *exec.Cmd
	if mode == "agent" {
		cmd = startAgentProcess(t, path)
	} else {
		cmd = startWorkflowProcess(t, path, "manual")
	}
	awaitLockAttempt(t, repository)
	id, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertAgentBusy := func() {
		t.Helper()
		if mode != "agent" {
			return
		}
		if lock, err := store.Agent(id); !errors.Is(err, runlock.ErrBusy) {
			if lock != nil {
				lock.Close()
			}
			t.Fatalf("agent lock was not retained: %v", err)
		}
	}
	assertWorkflowBusy(t, path)
	assertAgentBusy()
	cmd.Process.Kill()
	cmd.Wait()
	assertWorkflowBusy(t, path)
	assertAgentBusy()
	// There is no Cairn process left to release the lock. Borg's exit alone
	// must permit acquisition, even though it timed out on its repository.
	awaitWorkflowUnlock(t, path)
	if mode == "agent" {
		lock, err := store.Agent(id)
		if err != nil {
			t.Fatal(err)
		}
		lock.Close()
	}
}
