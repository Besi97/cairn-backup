package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAgentInvalidInputs(t *testing.T) {
	fakeBorgEnvironment(t)
	record := filepath.Join(t.TempDir(), "invoked")
	t.Setenv("FAKE_RECORD", record)
	manual := writeWorkflowConfig(t, `version: 1
repositories: {}
workflows: {}
`)
	for _, args := range [][]string{
		{"agent", "run", "--config", manual},
		{"agent", "run", "--config", ""},
		{"agent", "run", "--config", filepath.Join(t.TempDir(), "missing")},
		{"agent", "run", "unexpected", "--config", manual},
		{"agent", "run", "--repository", "repo"},
	} {
		var out bytes.Buffer
		if code := Run(t.Context(), args, &out, &out, "test"); code != 3 {
			t.Fatalf("%v: code=%d %s", args, code, &out)
		}
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid invocation ran Borg:", err)
	}
}

func TestAgentHelpAndCompletionDoNotLoadConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, args := range [][]string{
		{"agent", "--help"}, {"agent", "run", "--help", "--config", "/missing"},
		{"__complete", "agent", "run", "--config", ""},
	} {
		var out bytes.Buffer
		if code := Run(t.Context(), args, &out, &out, "test"); code != 0 {
			t.Fatalf("%v: code=%d %s", args, code, &out)
		}
	}
}

func TestAgentOutputAndLockSetupFailures(t *testing.T) {
	for _, mode := range []string{"output", "lock directory"} {
		t.Run(mode, func(t *testing.T) {
			fakeBorgEnvironment(t)
			path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  never:
    cron: "0 0 30 2 *"
    stages: [{tasks: [{type: compact, repository: local}]}]
`)
			var output bytes.Buffer
			if mode == "lock directory" {
				if err := os.Mkdir(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "cairn"), 0755); err != nil {
					t.Fatal(err)
				}
				if code := Run(t.Context(), []string{"agent", "run", "--config", path}, &output, &output, "test"); code != 2 {
					t.Fatalf("lock error: %d %s", code, &output)
				}
			} else if code := Run(t.Context(), []string{"agent", "run", "--config", path}, &output, brokenWriter{}, "test"); code != 2 {
				t.Fatalf("output error: %d %s", code, &output)
			}
		})
	}
}

func TestAgentProcessLifecycle(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			fakeBorgEnvironment(t)
			record := filepath.Join(t.TempDir(), "borg-invoked")
			t.Setenv("FAKE_RECORD", record)
			path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo}
workflows:
  never:
    cron: "0 0 30 2 *"
    stages: [{tasks: [{type: compact, repository: local}]}]
`)
			// Exercise discovery, not only --config, for the foreground process.
			xdg := t.TempDir()
			if err := os.Mkdir(filepath.Join(xdg, "cairn"), 0700); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(xdg, "cairn", "config.yaml"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_CONFIG_HOME", xdg)
			path = filepath.Join(xdg, "cairn", "config.yaml")
			logPath := filepath.Join(t.TempDir(), "agent-output")
			log, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(cairnBinary, "agent", "run")
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
			awaitAgentOutput(t, logPath, "agent: started")
			alias := filepath.Join(t.TempDir(), "alias.yaml")
			if err := os.Symlink(path, alias); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			duplicate := exec.CommandContext(ctx, cairnBinary, "agent", "run", "--config", alias)
			output, err := duplicate.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(output), "already running") ||
				strings.Contains(string(output), "for usage") {
				t.Fatalf("second agent: %v\n%s", err, output)
			}
			// The running agent must retain its snapshot and identity.
			if err := os.WriteFile(path, []byte("invalid replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if !errors.As(err, &exit) || exit.ExitCode() != 128+int(signal) {
				t.Fatalf("signal shutdown: %v", err)
			}
			awaitAgentOutput(t, logPath, "agent: stopped")
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("idle agent invoked Borg:", err)
			}
			// A replacement starts after orderly shutdown.
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			cancelled, stop := context.WithCancel(t.Context())
			writer := &cancelOnStart{cancel: stop}
			if code := Run(cancelled, []string{"agent", "run", "--config", path}, writer, writer, "test"); code != 143 {
				t.Fatalf("replacement agent: %d %s", code, &writer.Buffer)
			}
		})
	}
}

type cancelOnStart struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnStart) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.Contains(w.String(), "agent: started") {
		w.cancel()
	}
	return n, err
}

func awaitAgentOutput(t *testing.T, path, text string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), text) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("agent did not report %q: %s", text, data)
		case <-tick.C:
		}
	}
}
