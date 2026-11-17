package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/creack/pty"
)

func TestStandaloneLifecycle(t *testing.T) {
	fakeBorgEnvironment(t)
	for _, operation := range []string{"backup", "prune", "compact", "check"} {
		for _, code := range []int{0, 1, 2, 130} {
			t.Run(operation+"/"+strconv.Itoa(code), func(t *testing.T) {
				t.Setenv("FAKE_EXIT", strconv.Itoa(code))
				var events []lifecycle.Event
				ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
					events = append(events, event)
					return errors.New("secret delivery failure")
				})
				args := []string{operation, "--repository", "private-location"}
				switch operation {
				case "backup":
					args = append(args, "--archive-name", "private-name", "--dry-run", "private-source")
				case "prune":
					args = append(args, "--archive-prefix", "private-prefix", "--keep-daily", "1", "--dry-run")
				}
				var stdout, stderr bytes.Buffer
				if got := Run(ctx, args, &stdout, &stderr, "test"); got != code {
					t.Fatalf("code=%d want=%d stderr=%s", got, code, &stderr)
				}
				want := lifecycle.Success
				switch code {
				case 1:
					want = lifecycle.Warning
				case 2:
					want = lifecycle.Failure
				case 130:
					want = lifecycle.Interrupted
				}
				if len(events) != 4 || events[0].Kind != lifecycle.RunStarted ||
					events[1].Kind != lifecycle.TaskStarted || events[2].Kind != lifecycle.TaskFinished ||
					events[3].Kind != lifecycle.RunFinished || events[3].Outcome != want {
					t.Fatalf("events=%+v", events)
				}
				for _, event := range events {
					if event.Operation != operation || event.Source != lifecycle.Manual ||
						event.Preview != (operation == "backup" || operation == "prune") || event.Repository != "" {
						t.Fatalf("attribution=%+v", event)
					}
				}
				data, _ := json.Marshal(events)
				if strings.Contains(string(data), "private-") || strings.Contains(string(data), "diagnostic") ||
					strings.Contains(stderr.String(), "secret delivery") || !strings.Contains(stderr.String(), "Warning: lifecycle") {
					t.Fatalf("unsafe events or missing delivery report: %s stderr=%s", data, &stderr)
				}
			})
		}
	}
}

func TestCheckVerificationLifecycle(t *testing.T) {
	fakeBorgEnvironment(t)
	for _, verify := range []bool{false, true} {
		for _, configured := range []bool{false, true} {
			for _, code := range []int{0, 1, 2, 137} {
				t.Run(strconv.FormatBool(verify)+"/"+strconv.FormatBool(configured)+"/"+strconv.Itoa(code), func(t *testing.T) {
					t.Setenv("FAKE_EXIT", strconv.Itoa(code))
					args := []string{"check", "--repository", "private-location"}
					if verify {
						args = append(args, "--verify-data")
					}
					if configured {
						path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./private-location, passphrase: private-passphrase}
workflows:
  manual:
    stages:
      - tasks: [{type: check, repository: local, verify_data: `+strconv.FormatBool(verify)+`}]
      - tasks: [{type: compact, repository: local}]
`)
						args = []string{"workflow", "run", "manual", "--config", path}
					}
					var events []lifecycle.Event
					ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
						events = append(events, event)
						return errors.New("private-observer-error")
					})
					var stdout, stderr bytes.Buffer
					wantCode, want := code, lifecycle.Success
					switch code {
					case 1:
						want = lifecycle.Warning
					case 2:
						want = lifecycle.Failure
					case 137:
						want = lifecycle.Interrupted
						if configured {
							wantCode, want = 2, lifecycle.Failure
						}
					}
					if got := Run(ctx, args, &stdout, &stderr, "test"); got != wantCode {
						t.Fatalf("code=%d want=%d stderr=%s", got, wantCode, &stderr)
					}
					if events[len(events)-1].Outcome != want {
						t.Fatalf("events=%+v", events)
					}
					checks, skipped := 0, 0
					for _, event := range events {
						if event.Kind == lifecycle.TaskFinished && event.Operation == "check" {
							checks++
							taskOutcome := want
							if code == 137 {
								taskOutcome = lifecycle.Interrupted
							}
							if event.Task != 1 || event.Outcome != taskOutcome || event.Source != lifecycle.Manual || event.Preview {
								t.Fatalf("check attribution=%+v", event)
							}
							if configured && (event.Stage != 1 || event.Workflow != "manual" || event.Repository != "local") {
								t.Fatalf("configured check attribution=%+v", event)
							}
						}
						if event.Kind == lifecycle.TaskSkipped {
							skipped++
							reason := lifecycle.PriorFailure
							if code == 1 {
								reason = lifecycle.PriorWarning
							}
							if event.Stage != 2 || event.Operation != "compact" || event.Reason != reason {
								t.Fatalf("skip=%+v", event)
							}
						}
					}
					wantSkipped := 0
					if configured && code != 0 {
						wantSkipped = 1
					}
					data, _ := json.Marshal(events)
					if checks != 1 || skipped != wantSkipped || strings.Contains(string(data), "private-") ||
						!strings.Contains(stderr.String(), "Warning: lifecycle") {
						t.Fatalf("checks=%d skipped=%d events=%s stderr=%s", checks, skipped, data, &stderr)
					}
				})
			}
		}
	}
}

func TestConfiguredManualLifecycle(t *testing.T) {
	fakeBorgEnvironment(t)
	path := writeWorkflowConfig(t, workflowConfig)
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &stdout, &stderr, "test"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if len(events) != 14 || events[0].Kind != lifecycle.RunStarted || events[13].Kind != lifecycle.RunFinished {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if event.Workflow != "manual" || event.Source != lifecycle.Manual || event.RunID != events[0].RunID {
			t.Fatalf("attribution=%+v", event)
		}
	}
}

func TestNonRunCommandsDoNotEmitEvents(t *testing.T) {
	path := writeWorkflowConfig(t, workflowConfig)
	for _, args := range [][]string{
		{"backup"}, {"check"}, {"check", "--help"}, {"--help"}, {"--version"},
		nil, {"tui"}, {"workflow", "list", "--config", path}, {"config", "check", "--config", path},
	} {
		ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
			t.Errorf("unexpected event for %v: %+v", args, event)
			return nil
		})
		var output bytes.Buffer
		Run(ctx, args, &output, &output, "test")
	}
}

func TestTUIBrowsingDoesNotEmitLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", "")
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(lifecycle.Event{RunID: "history-pty-run", Sequence: 1, Time: time.Now(),
		Source: lifecycle.Manual, Kind: lifecycle.RunStarted, Operation: "check"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	snapshot := filepath.Join(os.Getenv("XDG_STATE_HOME"), "cairn/history/runs.json")
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := writeWorkflowConfig(t, workflowConfig)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var events []lifecycle.Event
	ctx = lifecycle.WithObserver(ctx, func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	chunks := make(chan string)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				select {
				case chunks <- string(buf[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	cmd := newRootCommand("test")
	cmd.SetArgs([]string{"tui", "--config", path})
	cmd.SetIn(slave)
	cmd.SetOut(slave)
	cmd.SetErr(io.Discard)
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- cmd.ExecuteContext(ctx)
	}()
	defer func() {
		cancel()
		timeout := time.NewTimer(2 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case <-chunks:
			case <-exited:
				return
			case <-timeout.C:
				t.Error("TUI cleanup did not finish")
				return
			}
		}
	}()
	escapes := regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	waitFor := func(text string) {
		t.Helper()
		output := ""
		for !strings.Contains(strings.Join(strings.Fields(escapes.ReplaceAllString(output, "")), " "), text) {
			select {
			case chunk := <-chunks:
				output += chunk
			case err := <-done:
				t.Fatalf("TUI exited before %q: %v (%q)", text, err, output)
			case <-ctx.Done():
				t.Fatalf("TUI did not show %q: %q", text, output)
			}
		}
	}
	send := func(keys string) {
		t.Helper()
		if _, err := io.WriteString(master, keys); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("Configuration:")
	send("\t")
	waitFor("> manual")
	send("\r")
	waitFor("Task 1:")
	send("\r")
	waitFor("enter repository")
	send("\r")
	waitFor("Referenced by manual")
	send("y")
	waitFor("Section: repositories / local")
	send("r")
	waitFor("TUI configuration reloaded.")
	if err := os.WriteFile(path, []byte("invalid: configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	send("r")
	waitFor("cannot read or validate")
	send("\t")
	waitFor("unknown · incomplete")
	send("\r")
	waitFor("Recorded outcome: unknown")
	send("g")
	send("\x1b")
	waitFor("> check")
	send("q")
	// Drain terminal output while Bubble Tea restores the screen.
	for {
		select {
		case <-chunks:
		case err := <-done:
			if err != nil || len(events) != 0 {
				t.Fatalf("TUI error=%v unexpected events=%+v", err, events)
			}
			after, readErr := os.ReadFile(snapshot)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("TUI changed history: %v", readErr)
			}
			return
		case <-ctx.Done():
			t.Fatal("TUI did not exit")
		}
	}
}
