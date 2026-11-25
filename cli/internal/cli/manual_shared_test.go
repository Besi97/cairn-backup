package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

func TestSharedManualOutcomes(t *testing.T) {
	for _, tc := range []struct {
		exit    string
		outcome borg.Outcome
		stages  int
	}{
		{"0", borg.Success, 3},
		{"1", borg.Warning, 1},
		{"2", borg.Failure, 1},
	} {
		t.Run(tc.exit, func(t *testing.T) {
			fakeBorgEnvironment(t)
			lockEnvironment(t)
			path := writeWorkflowConfig(t, workflowConfig)
			calls := filepath.Join(t.TempDir(), "calls")
			t.Setenv("FAKE_CALLS", calls)
			t.Setenv("FAKE_EXIT", tc.exit)
			options, err := config.Environment()
			if err != nil {
				t.Fatal(err)
			}
			options.Path, options.Explicit = path, true
			selected, err := config.Load(options)
			if err != nil {
				t.Fatal(err)
			}
			manual, err := workflow.PrepareManual(selected, "manual", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(calls); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("preparation started work: %v", err)
			}
			result, err := manual.Run(t.Context(), workflow.Executor{Stdout: io.Discard, Stderr: io.Discard})
			if (err != nil) != (tc.outcome == borg.Failure) || result.Outcome != tc.outcome || len(result.Stages) != tc.stages {
				t.Fatalf("result: %+v %v", result, err)
			}
		})
	}
}

type rejectReplacementWriter struct {
	bytes.Buffer
}

func (w *rejectReplacementWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("�")) {
		return 0, errors.New("diagnostic output failed")
	}
	return w.Buffer.Write(p)
}

func TestManualBorgIncompleteUTF8Diagnostics(t *testing.T) {
	for _, operation := range []string{"backup", "restore"} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reject=%t", operation, reject), func(t *testing.T) {
				var args []string
				if operation == "restore" {
					fakeRestoreData(t)
					args = append(restoreArgs(filepath.Join(t.TempDir(), "destination")), "--dry-run")
				} else {
					fakeBorgEnvironment(t)
					args = backupArgs()
				}
				t.Setenv("FAKE_STDOUT", "trailing \xe2\x82")
				var stdout bytes.Buffer
				stderr := &rejectReplacementWriter{}
				var diagnosticOutput io.Writer = &stderr.Buffer
				if reject {
					diagnosticOutput = stderr
				}
				code := Run(t.Context(), args, &stdout, diagnosticOutput, "test")
				if reject {
					if code != exitFailure || !strings.Contains(stdout.String(), "failed") {
						t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, stderr.String())
					}
				} else if code != exitSuccess || !strings.Contains(stderr.String(), "borg ["+operation+"] trailing ��\n") || !strings.Contains(stdout.String(), "completed") {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, stderr.String())
				}
			})
		}
	}
}

func TestSharedManualCancellationAndCLILocking(t *testing.T) {
	fakeBorgEnvironment(t)
	lockEnvironment(t)
	path := writeWorkflowConfig(t, workflowConfig)
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("FAKE_RECORD", ready)
	t.Setenv("FAKE_RELEASE", ready+".release")
	options, err := config.Environment()
	if err != nil {
		t.Fatal(err)
	}
	options.Path, options.Explicit = path, true
	selected, err := config.Load(options)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := workflow.PrepareManual(selected, "manual", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	recorder := &historyRecorder{warn: func() { t.Error("history unavailable") }}
	defer recorder.close()
	ctx, cancel := context.WithCancel(lifecycle.WithAdditionalObserver(t.Context(), recorder.observe))
	defer cancel()
	type response struct {
		result workflow.Result
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := manual.Run(ctx, workflow.Executor{Stdout: io.Discard, Stderr: io.Discard})
		done <- response{result, err}
	}()
	awaitFile(t, ready)
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, &out, &stderr, "test"); code != 2 || !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("busy CLI: %d %s", code, &stderr)
	}
	cancel()
	select {
	case got := <-done:
		if got.result.Outcome != borg.Interrupted || !errors.Is(got.err, context.Canceled) || len(got.result.Stages) != 1 {
			t.Fatalf("cancelled run: %+v %v", got.result, got.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancelled run did not stop")
	}
	awaitWorkflowUnlock(t, path)
	store, err := history.OpenReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Summary.Workflow == "manual" && row.Summary.Outcome == lifecycle.Interrupted && !row.Summary.Incomplete {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing interrupted history: %+v", rows)
	}
}
