package workflow

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/drill"
	"github.com/Besi97/cairn-backup/cli/internal/recovery/selection"
)

func TestConfiguredDrillExecution(t *testing.T) {
	runner, _ := drillFixture(t, "")
	dir := t.TempDir()
	text := `version: 1
repositories:
  remote: {location: "user@host:repo", passphrase: "secret", identity_file: ./key}
workflows:
  scheduled:
    cron: "@daily"
    stages:
      - tasks:
          - type: recovery_check
            repository: remote
            archive_prefix: secret
            paths: [secret-path]
            checks:
              - {kind: file_bytes, path: secret-path, bytes: restored}
              - {kind: file_size, path: secret-path, size: 8}
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(filepath.Join(dir, "key"), []byte("test key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(selected, "scheduled", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	opts := plan.Stages[0][0].Drill
	if opts == nil || opts.Selector.LatestPrefix != "secret" || opts.Repository.Access.IdentityFile != filepath.Join(dir, "key") || *opts.Repository.Access.Passphrase != "secret" || string(opts.Checks[0].Bytes) != "restored" {
		t.Fatal("incorrect drill preparation")
	}
	var output bytes.Buffer
	result, err := (Executor{Runner: runner, Stdout: &output, Stderr: &output}).Run(t.Context(), plan)
	if err != nil || result.Outcome != borg.Success {
		t.Fatalf("result %+v err %v output %s", result, err, output.String())
	}
}

func drillFixture(t *testing.T, behavior string) (borg.Runner, drill.Options) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DRILL_FIXTURE", dir)
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if err := tw.WriteHeader(&tar.Header{Name: "secret-path", Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"repository":{"id":"%s"},"archives":[{"barchive":"secret-archive","id":"%s"}]}`, strings.Repeat("b", 64), strings.Repeat("a", 64))
	for name, content := range map[string][]byte{"tar": data.Bytes(), "manifest": []byte(manifest), "borg": []byte(`#!/bin/sh
set -eu
	if [ "$1" = "--version" ]; then echo "borg 1.4.5"; exit; fi
if [ "$1" = "--rsh" ]; then shift 2; fi
` + behavior + `
case "$1" in
 list) case " $* " in *" --json "*) cat "$DRILL_FIXTURE/manifest";; *) printf '%s\0%s\0' '-' '-rw-------';; esac;;
 export-tar) cat "$DRILL_FIXTURE/tar";;
 extract) printf restored > secret-path;;
 *) exit 2;;
esac
`)} {
		mode := os.FileMode(0600)
		if name == "borg" {
			mode = 0700
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, mode); err != nil {
			t.Fatal(err)
		}
	}
	passphrase := "secret-credential"
	return borg.Runner{Executable: filepath.Join(dir, "borg")}, drill.Options{Repository: borg.PreparedRepository{Location: filepath.Join(dir, "repo"), Access: borg.AccessOptions{Passphrase: &passphrase}}, Dir: dir, Selector: selection.Selector{LatestPrefix: "secret"}, Paths: []string{"secret-path"}}
}

func TestDrillWorkflowEvidenceAndStages(t *testing.T) {
	for _, tc := range []struct {
		name, behavior string
		sample, fail   bool
		outcome        borg.Outcome
		evidence       lifecycle.Outcome
	}{
		{name: "success", outcome: borg.Success, evidence: lifecycle.Success},
		{name: "warning", sample: true, outcome: borg.Warning, evidence: lifecycle.Warning},
		{name: "failure", behavior: `if [ "$1" = extract ]; then exit 1; fi`, fail: true, outcome: borg.Failure, evidence: lifecycle.Failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner, opts := drillFixture(t, tc.behavior)
			if tc.sample {
				live := t.TempDir()
				if err := os.WriteFile(filepath.Join(live, "secret-path"), []byte("different"), 0600); err != nil {
					t.Fatal(err)
				}
				opts.Sample = &drill.SampleOptions{LiveRoot: live, Limit: 1}
			}
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			store, err := history.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var events []lifecycle.Event
			ctx := lifecycle.WithObserver(t.Context(), func(e lifecycle.Event) error { events = append(events, e); return store.Observe(e) })
			var output bytes.Buffer
			task := Task{Type: "recovery", Repository: "repo", Scope: "recovery drill", Drill: &opts}
			result, err := (Executor{Runner: runner, Stdout: &output, Stderr: &output}).Run(ctx, Plan{Name: "test", Stages: [][]Task{{task, task}, {task}}})
			if (err != nil) != tc.fail || result.Outcome != tc.outcome {
				t.Fatalf("result %+v err %v output %s", result, err, output.String())
			}
			expectedStages := 1
			if tc.outcome == borg.Success {
				expectedStages = 2
			}
			if len(result.Stages) != expectedStages {
				t.Fatalf("stages: %+v", result.Stages)
			}
			finished := 0
			for _, e := range events {
				if e.Kind == lifecycle.TaskFinished {
					finished++
					if e.Recovery == nil || e.Outcome != tc.evidence || e.Recovery.Archive == nil {
						t.Fatalf("event %+v", e)
					}
				}
			}
			if finished != 2+(expectedStages-1) {
				t.Fatalf("finished %d", finished)
			}
			if !strings.Contains(output.String(), "cairn [test 1.1 recovery/repo]") || !strings.Contains(output.String(), "Recovery evidence v1") || strings.Contains(output.String(), "secret-credential") {
				t.Fatalf("output %s", output.String())
			}
			runs, err := store.List()
			if err != nil || len(runs) != 1 || len(runs[0].Recovery) != finished {
				t.Fatalf("history %+v err %v", runs, err)
			}
			raw, err := os.ReadFile(filepath.Join(state, "cairn", "history", "runs.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "secret") {
				t.Fatalf("history leaked sensitive data: %s", raw)
			}
		})
	}
}

func TestDrillWorkflowCancellation(t *testing.T) {
	runner, opts := drillFixture(t, `if [ "$1" = list ]; then touch "$DRILL_FIXTURE/started"; exec sleep 30; fi`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var events []lifecycle.Event
	ctx = lifecycle.WithObserver(ctx, func(e lifecycle.Event) error { events = append(events, e); return nil })
	done := make(chan struct{})
	var result Result
	var err error
	task := Task{Type: "recovery", Repository: "repo", Scope: "drill", Drill: &opts}
	go func() {
		result, err = (Executor{Runner: runner}).Run(ctx, Plan{Name: "test", Stages: [][]Task{{task}, {task}}})
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(opts.Dir, "started")); e == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("drill did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if !errors.Is(err, context.Canceled) || result.Outcome != borg.Interrupted {
		t.Fatalf("result %+v err %v", result, err)
	}
	found := false
	for _, e := range events {
		if e.Kind == lifecycle.TaskFinished {
			found = true
			if e.Outcome != lifecycle.Interrupted || e.Recovery == nil || e.Recovery.Outcome.Status != "interrupted" {
				t.Fatalf("event %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("missing finished event")
	}
}

func TestDrillWorkflowCredentialErrorIsRedacted(t *testing.T) {
	runner, opts := drillFixture(t, "")
	opts.Repository.Access.Passphrase = nil
	opts.Repository.Access.PassphraseFile = filepath.Join(opts.Dir, "secret-credential-file")
	var output bytes.Buffer
	var finished lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(e lifecycle.Event) error {
		if e.Kind == lifecycle.TaskFinished {
			finished = e
		}
		return nil
	})
	result, err := (Executor{Runner: runner, Stdout: &output, Stderr: &output}).Run(ctx, Plan{Name: "test", Stages: [][]Task{{{Type: "recovery", Repository: "repo", Scope: "drill", Drill: &opts}}}})
	if err == nil || result.Outcome != borg.Failure || strings.Contains(err.Error(), "secret-credential") || strings.Contains(output.String(), "secret-credential") {
		t.Fatalf("result %+v err %v output %s", result, err, output.String())
	}
	if finished.Recovery == nil || finished.Outcome != lifecycle.Failure {
		t.Fatalf("event %+v", finished)
	}
}
