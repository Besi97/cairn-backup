package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func TestStandaloneLifecycle(t *testing.T) {
	fakeBorgEnvironment(t)
	for _, operation := range []string{"backup", "prune", "compact"} {
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
						event.Preview != (operation != "compact") || event.Repository != "" {
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
	for _, args := range [][]string{{"backup"}, {"--help"}, {"--version"}} {
		ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
			t.Errorf("unexpected event for %v: %+v", args, event)
			return nil
		})
		var output bytes.Buffer
		Run(ctx, args, &output, &output, "test")
	}
}
