package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

// Stand in for #80's agent, exercising the exact shared lock/executor API
// without implementing a scheduler or adding a hidden production command.
func agentLockHelper() int {
	if len(os.Args) != 2 {
		return 3
	}
	options, err := config.Environment()
	if err != nil {
		return 3
	}
	options.Path, options.Explicit = os.Args[1], true
	selected, err := config.Load(options)
	if err != nil {
		return 3
	}
	id, err := runlock.Config(selected.Path)
	if err != nil {
		return 2
	}
	store, err := runlock.Open()
	if err != nil {
		return 2
	}
	defer store.Close()
	agent, err := store.Agent(id)
	if err != nil {
		return 2
	}
	defer agent.Close()
	plan, err := workflow.Prepare(selected, "manual", time.Now())
	if err != nil {
		return 3
	}
	_, err = (workflow.Executor{AgentLock: agent, Stdout: os.Stdout, Stderr: os.Stderr}).RunLocked(context.Background(), id, plan)
	if err != nil {
		return 2
	}
	return 0
}

func lockEnvironment(t *testing.T) {
	t.Helper()
	runtime := t.TempDir()
	if err := os.Chmod(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

// Run the real CLI entry point in an independently killable process. Use files
// rather than parent-owned output pipes so an orphan cannot hang Cmd.Wait.
func startWorkflowProcess(t *testing.T, path, name string) *exec.Cmd {
	t.Helper()
	log, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	cmd := exec.Command(cairnBinary, "workflow", "run", name, "--config", path)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd
}

func startAgentProcess(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(t.TempDir(), "agent-output"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	cmd := exec.Command(binary, path)
	cmd.Env = append(os.Environ(), "CAIRN_TEST_AGENT_HELPER=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return cmd
}

func assertWorkflowBusy(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cairnBinary, "workflow", "run", "manual", "--config", path)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(output), "already running") ||
		strings.Contains(string(output), "for usage") {
		t.Fatalf("duplicate: %v\n%s", err, output)
	}
}

func awaitWorkflowUnlock(t *testing.T, path string) {
	t.Helper()
	id, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		lock, err := store.Workflow(id, "manual")
		if err == nil {
			lock.Close()
			return
		}
		if !errors.Is(err, runlock.ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("workflow did not release lock: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkflowCrossProcessLocks(t *testing.T) {
	for _, finish := range []string{"complete", "warning", "failure", "cancel", "crash", "fallback crash", "probe crash", "agent crash"} {
		t.Run(finish, func(t *testing.T) {
			fakeBorgEnvironment(t)
			if finish == "fallback crash" {
				t.Setenv("XDG_RUNTIME_DIR", "")
			}
			if finish == "warning" {
				t.Setenv("FAKE_EXIT", "1")
			}
			if finish == "failure" {
				t.Setenv("FAKE_EXIT", "2")
			}
			base := filepath.Join(t.TempDir(), "ready")
			t.Setenv("FAKE_RECORD", base)
			t.Setenv("FAKE_RELEASE", base+".release")
			if finish == "probe crash" {
				t.Setenv("FAKE_TREE", base)
				t.Setenv("FAKE_BLOCK_PROBE", "yes")
			}
			path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    cron: "0 0 30 2 *"
    stages:
      - tasks: [{type: compact, repository: local}]
`)
			var cmd *exec.Cmd
			if finish == "agent crash" {
				cmd = startAgentProcess(t, path)
			} else {
				cmd = startWorkflowProcess(t, path, "manual")
			}
			pid := 0
			if finish == "probe crash" {
				awaitFile(t, base+".parent")
				data, _ := os.ReadFile(base + ".parent")
				pid, _ = strconv.Atoi(string(data))
			} else {
				awaitFile(t, base)
				var record struct{ PID int }
				data, _ := os.ReadFile(base)
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				pid = record.PID
			}
			if pid <= 0 {
				t.Fatal("missing Borg child PID")
			}
			active := true
			t.Cleanup(func() {
				if active {
					syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
			assertWorkflowBusy(t, path)
			alias := filepath.Join(t.TempDir(), "alias.yaml")
			if err := os.Symlink(path, alias); err != nil {
				t.Fatal(err)
			}
			assertWorkflowBusy(t, alias)
			assertAgentHeld := func() {
				t.Helper()
				if finish != "agent crash" {
					return
				}
				id, err := runlock.Config(path)
				if err != nil {
					t.Fatal(err)
				}
				store, err := runlock.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if lock, err := store.Agent(id); !errors.Is(err, runlock.ErrBusy) {
					if lock != nil {
						lock.Close()
					}
					t.Fatalf("agent lock: %v", err)
				}
			}
			assertAgentHeld()
			var recorded history.Summary
			inspectOwner := func(want runlock.OwnerState) {
				t.Helper()
				if finish == "agent crash" {
					return // This library-only fixture does not attach a history observer.
				}
				s, err := history.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				rows, err := s.Inspect()
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					r := row.Summary
					if r.Execution != nil && r.Execution.PID == cmd.Process.Pid {
						if !r.Incomplete || r.Outcome != "" || r.FinishedAt != nil || row.Owner != want {
							t.Fatalf("invented state: %+v", row)
						}
						if recorded.RunID != "" && (r.Sequence != recorded.Sequence || !r.LastObserved.Equal(recorded.LastObserved)) {
							t.Fatal("inspection rewrote history")
						}
						recorded = r
						return
					}
				}
				t.Fatal("missing owner evidence")
			}
			inspectOwner(runlock.OwnerPresent)
			switch finish {
			case "crash", "fallback crash", "probe crash", "agent crash":
				cmd.Process.Kill()
				cmd.Wait()
				assertWorkflowBusy(t, path)
				assertAgentHeld()
				inspectOwner(runlock.OwnerGone)
				syscall.Kill(-pid, syscall.SIGKILL)
			case "cancel":
				cmd.Process.Signal(syscall.SIGTERM)
				err := cmd.Wait()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 143 {
					t.Fatalf("cancellation: %v", err)
				}
			default:
				if err := os.WriteFile(base+".release", nil, 0600); err != nil {
					t.Fatal(err)
				}
				err := cmd.Wait()
				want := 0
				if finish == "warning" {
					want = 1
				}
				if finish == "failure" {
					want = 2
				}
				if cmd.ProcessState.ExitCode() != want {
					t.Fatalf("exit = %v; want %d", err, want)
				}
			}
			awaitWorkflowUnlock(t, path)
			active = false
			if finish == "crash" {
				inspectOwner(runlock.OwnerGone)
				if err := os.WriteFile(base+".release", nil, 0600); err != nil {
					t.Fatal(err)
				}
				restarted := startWorkflowProcess(t, path, "manual")
				if err := restarted.Wait(); err != nil {
					t.Fatal(err)
				}
				inspectOwner(runlock.OwnerGone)
			}
		})
	}
}

func TestIndependentWorkflowProcesses(t *testing.T) {
	fakeBorgEnvironment(t)
	base := t.TempDir()
	release := filepath.Join(base, "release")
	t.Setenv("FAKE_RELEASE", release)
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    stages: [{tasks: [{type: compact, repository: local}]}]
  other:
    stages: [{tasks: [{type: compact, repository: local}]}]
`)
	copyPath := filepath.Join(base, "copy.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	var processes []*exec.Cmd
	for i, spec := range []struct{ path, name string }{{path, "manual"}, {path, "other"}, {copyPath, "manual"}} {
		record := filepath.Join(base, fmt.Sprintf("ready-%d", i))
		t.Setenv("FAKE_RECORD", record)
		processes = append(processes, startWorkflowProcess(t, spec.path, spec.name))
		awaitFile(t, record)
	}
	assertWorkflowBusy(t, path)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range processes {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScheduledCallerObservesManualLock(t *testing.T) {
	fakeBorgEnvironment(t)
	base := filepath.Join(t.TempDir(), "ready")
	t.Setenv("FAKE_RECORD", base)
	t.Setenv("FAKE_RELEASE", base+".release")
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    stages: [{tasks: [{type: compact, repository: local}]}]
`)
	cmd := startWorkflowProcess(t, path, "manual")
	awaitFile(t, base)
	id, err := runlock.Config(path)
	if err != nil {
		t.Fatal(err)
	}
	// #80 consumes ErrBusy as a skip, not as a run result or a retry.
	var output bytes.Buffer
	result, err := (workflow.Executor{Stdout: &output, Stderr: &output}).RunLocked(
		t.Context(), id, workflow.Plan{Name: "manual"})
	if !errors.Is(err, runlock.ErrBusy) || len(result.Stages) != 0 || output.Len() != 0 {
		t.Fatalf("scheduled overlap: %+v, %v, %s", result, err, &output)
	}
	if err := os.WriteFile(base+".release", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLockSetupAndLaunchFailures(t *testing.T) {
	for _, mode := range []string{"unsafe directory", "missing Borg"} {
		t.Run(mode, func(t *testing.T) {
			fakeBorgEnvironment(t)
			record := filepath.Join(t.TempDir(), "invoked")
			t.Setenv("FAKE_RECORD", record)
			path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    stages: [{tasks: [{type: compact, repository: local}]}]
`)
			if mode == "unsafe directory" {
				if err := os.Mkdir(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cairn"), 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			for range 2 {
				var out, stderr bytes.Buffer
				code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, &out, &stderr, "test")
				if code != 2 || strings.Contains(stderr.String(), "already running") || strings.Contains(stderr.String(), "for usage") {
					t.Fatalf("failure: code=%d out=%q err=%q", code, &out, &stderr)
				}
			}
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("Borg ran despite setup failure:", err)
			}
		})
	}
}
