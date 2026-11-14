package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func writeWorkflowConfig(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const workflowConfig = `version: 1
repositories:
  local: {location: ./repo}
workflows:
  manual:
    stages:
      - tasks:
          - {type: backup, repository: local, archive_name: docs-current, sources: [.]}
      - tasks:
          - {type: prune, repository: local, archive_prefix: docs, keep_daily: 1}
      - tasks:
          - {type: compact, repository: local}
`

func TestWorkflowCLI(t *testing.T) {
	for _, tt := range []struct {
		exit  string
		code  int
		want  string
		calls int
	}{
		{"0", 0, `Workflow "manual" completed.`, 3},
		{"1", 1, "completed with warnings; 2 later stage(s) not started", 1},
		{"100", 1, "completed with warnings; 2 later stage(s) not started", 1},
		{"2", 2, "failed; 2 later stage(s) not started", 1},
		{"137", 2, "interrupted (signal 9)", 1},
	} {
		t.Run(tt.exit, func(t *testing.T) {
			fakeBorgEnvironment(t)
			path := writeWorkflowConfig(t, workflowConfig)
			calls := filepath.Join(t.TempDir(), "calls")
			t.Setenv("FAKE_CALLS", calls)
			t.Setenv("FAKE_EXIT", tt.exit)
			var stdout, stderr bytes.Buffer
			code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, &stdout, &stderr, "test")
			if code != tt.code || !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("code=%d out=%q err=%q", code, &stdout, &stderr)
			}
			file, err := os.Open(calls)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			decoder := json.NewDecoder(file)
			var got [][]string
			for {
				var args []string
				if err := decoder.Decode(&args); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				got = append(got, args)
			}
			if len(got) != tt.calls || got[0][0] != "create" {
				t.Fatalf("calls=%q", got)
			}
			if tt.calls == 3 && (got[1][0] != "prune" || got[2][0] != "compact") {
				t.Fatalf("calls out of order: %q", got)
			}
		})
	}
}

func TestWorkflowDiscoveryAndInvalidInput(t *testing.T) {
	fakeBorgEnvironment(t)
	path := writeWorkflowConfig(t, workflowConfig)
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "cairn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cairn/config.yaml"), []byte(workflowConfig), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		code int
	}{
		{[]string{"workflow", "run", "manual"}, 0},
		{[]string{"workflow", "run", "Manual", "--config", path}, 3},
		{[]string{"workflow", "run"}, 3},
		{[]string{"workflow", "run", "manual", "extra"}, 3},
		{[]string{"workflow", "run", "manual", "--config", ""}, 3},
		{[]string{"workflow", "run", "manual", "--config", path + ".missing"}, 3},
		{[]string{"workflow", "run", "manual", "--repository", "./other"}, 3},
		{[]string{"workflow", "run", "manual", "--dry-run"}, 3},
	} {
		var out, stderr bytes.Buffer
		if code := Run(t.Context(), tt.args, &out, &stderr, "test"); code != tt.code {
			t.Fatalf("args=%q code=%d err=%q", tt.args, code, &stderr)
		}
	}
	// Full-file validation must fail before the selected workflow starts Borg.
	t.Setenv("PATH", t.TempDir())
	invalid := strings.Replace(workflowConfig, "workflows:", "workflows:\n  invalid: {stages: []}", 1)
	path = writeWorkflowConfig(t, invalid)
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, &out, &stderr, "test"); code != 3 || strings.Contains(stderr.String(), "find Borg") {
		t.Fatalf("code=%d err=%q", code, &stderr)
	}
}

func TestWorkflowList(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local:
    location: ./missing-repository
    passphrase_file: ./missing-secret
workflows:
  zeta:
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_name: snapshot
            source_root: ./missing-source-root
            sources: [./missing-source]
  alpha:
    stages:
      - tasks: [{type: compact, repository: local}]
  middle:
    stages:
      - tasks: [{type: compact, repository: local}]
`)
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "list", "--config", path}, &out, &stderr, "test"); code != 0 {
		t.Fatalf("code=%d out=%q err=%q", code, &out, &stderr)
	}
	if got, want := out.String(), "alpha\nmiddle\nzeta\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", &stderr)
	}
}

func TestWorkflowListDiscoveryEmptyAndInvalidInput(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "cairn"), 0700); err != nil {
		t.Fatal(err)
	}
	discovered := `version: 1
repositories:
  local: {location: ./missing-repository}
workflows:
  discovered:
    stages:
      - tasks: [{type: compact, repository: local}]
`
	if err := os.WriteFile(filepath.Join(home, "cairn/config.yaml"), []byte(discovered), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "list"}, &out, &stderr, "test"); code != 0 || out.String() != "discovered\n" {
		t.Fatalf("discovered list code=%d out=%q err=%q", code, &out, &stderr)
	}

	// The invalid repository reference ensures the loader validates the complete
	// file rather than treating listing as a partial parse.
	invalid := strings.Replace(discovered, "repositories:\n  local: {location: ./missing-repository}", "repositories: {}", 1)
	if err := os.WriteFile(filepath.Join(home, "cairn/config.yaml"), []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := Run(t.Context(), []string{"workflow", "list"}, &out, &stderr, "test"); code != 3 {
		t.Fatalf("invalid discovered config code=%d out=%q err=%q", code, &out, &stderr)
	}

	empty := writeWorkflowConfig(t, `version: 1
repositories: {}
workflows: {}
`)
	out.Reset()
	stderr.Reset()
	if code := Run(t.Context(), []string{"workflow", "list", "--config", empty}, &out, &stderr, "test"); code != 0 || out.Len() != 0 {
		t.Fatalf("empty list code=%d out=%q err=%q", code, &out, &stderr)
	}

	valid := writeWorkflowConfig(t, `version: 1
repositories: {}
workflows: {}
`)
	for _, args := range [][]string{
		{"workflow", "list", "extra", "--config", valid},
		{"workflow", "list", "--config", ""},
		{"workflow", "list", "--config", valid + ".missing"},
	} {
		out.Reset()
		stderr.Reset()
		if code := Run(t.Context(), args, &out, &stderr, "test"); code != 3 {
			t.Fatalf("args=%q code=%d out=%q err=%q", args, code, &out, &stderr)
		}
	}
}

func TestWorkflowListOutputFailure(t *testing.T) {
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./unused}
workflows:
  listed:
    stages:
      - tasks: [{type: compact, repository: local}]
`)
	var stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "list", "--config", path}, brokenWriter{}, &stderr, "test"); code != 2 {
		t.Fatalf("code=%d stderr=%q", code, &stderr)
	}
}

func TestWorkflowCompactIgnoresConfiguredPassphraseFile(t *testing.T) {
	fakeBorgEnvironment(t)
	path := writeWorkflowConfig(t, `version: 1
repositories:
  local: {location: ./repo, passphrase_file: ./does-not-exist}
workflows:
  manual:
    stages:
      - tasks: [{type: compact, repository: local}]
`)
	var out, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, &out, &stderr, "test"); code != 0 {
		t.Fatalf("code=%d err=%q", code, &stderr)
	}
}

func TestWorkflowCancellation(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			fakeBorgEnvironment(t)
			base := filepath.Join(t.TempDir(), "ready")
			t.Setenv("FAKE_TREE", base)
			path := writeWorkflowConfig(t, workflowConfig)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			done := make(chan int, 1)
			go func() {
				done <- Run(ctx, []string{"workflow", "run", "manual", "--config", path}, &stdout, &stderr, "test")
			}()
			awaitFile(t, base+".parent")
			awaitFile(t, base+".child")
			cancel(cancellation.Signal(signal))
			select {
			case code := <-done:
				if code != 128+int(signal) || !strings.Contains(stdout.String(), "2 later stage(s) not started") {
					t.Fatalf("code=%d out=%q err=%q", code, &stdout, &stderr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("workflow cancellation hung")
			}
			for _, role := range []string{"parent", "child"} {
				data, err := os.ReadFile(base + "." + role + ".signal")
				if err != nil || string(data) != strconv.Itoa(int(signal)) {
					t.Fatalf("%s received %s: %v", role, data, err)
				}
			}
		})
	}
}

func TestWorkflowOutputFailures(t *testing.T) {
	fakeBorgEnvironment(t)
	path := writeWorkflowConfig(t, workflowConfig)
	for _, stderr := range []bool{false, true} {
		var output bytes.Buffer
		out, diagnostics := io.Writer(&output), io.Writer(brokenWriter{})
		if !stderr {
			out, diagnostics = diagnostics, out
		}
		if code := Run(t.Context(), []string{"workflow", "run", "manual", "--config", path}, out, diagnostics, "test"); code != 2 {
			t.Fatalf("code=%d output=%q", code, &output)
		}
	}
}
