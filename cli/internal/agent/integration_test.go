package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

func TestMain(m *testing.M) {
	if path := os.Getenv("CAIRN_SCHEDULED_AGENT_HELPER"); path != "" {
		os.Exit(scheduledAgentHelper(path))
	}
	if os.Getenv("CAIRN_AGENT_HOLDER") == "1" {
		if err := os.WriteFile(os.Getenv("CAIRN_AGENT_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		for {
			if _, err := os.Stat(os.Getenv("CAIRN_AGENT_RELEASE")); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

type observedOutput struct {
	ctx     context.Context
	mu      sync.Mutex
	buffer  bytes.Buffer
	changed chan struct{}
}

func (o *observedOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buffer.Write(p)
	select {
	case o.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (o *observedOutput) text() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}

func (o *observedOutput) await(t *testing.T, text string) {
	t.Helper()
	for !strings.Contains(o.text(), text) {
		select {
		case <-o.changed:
		case <-o.ctx.Done():
			t.Fatalf("did not report %q:\n%s", text, o.text())
		}
	}
}

func TestObservedOutputUsesAgentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		output := &observedOutput{ctx: ctx, changed: make(chan struct{}, 1)}
		start := time.Now()
		go func() {
			time.Sleep(21 * time.Second)
			_, _ = output.Write([]byte("workflow completed"))
		}()
		output.await(t, "workflow completed")
		if elapsed := time.Since(start); elapsed != 21*time.Second {
			t.Fatalf("observation returned after %s, want 21s", elapsed)
		}
	})
}

func TestObservedAgentShutdown(t *testing.T) {
	a, _ := newTestAgent(t, definition("nightly", "* * * * *", "UTC"), instant("2026-01-01T00:00:00Z"))
	output, cancel, _ := runObserved(t, a)
	cancel()
	output.await(t, "agent: stopped")
}

func realBorg(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to exercise scheduled real Borg operations")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(bin, "borg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for _, name := range []string{"BORG_PASSPHRASE", "BORG_PASSCOMMAND", "BORG_PASSPHRASE_FD"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	state := t.TempDir()
	for _, name := range []string{"BORG_BASE_DIR", "BORG_CONFIG_DIR", "BORG_CACHE_DIR", "BORG_SECURITY_DIR", "BORG_KEYS_DIR"} {
		t.Setenv(name, filepath.Join(state, name))
	}
	t.Setenv("BORG_HOST_ID", "cairn-agent-integration")
	t.Setenv("BORG_UNKNOWN_UNENCRYPTED_REPO_ACCESS_IS_OK", "yes")
	t.Setenv("BORG_RELOCATED_REPO_ACCESS_IS_OK", "no")
	t.Setenv("BORG_EXIT_CODES", "modern")
	return binary
}

func borgCommand(t *testing.T, binary string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Borg %s: %v\n%s\n%s", args[0], err, &stdout, &stderr)
	}
	return stdout.Bytes()
}

func runObserved(t *testing.T, a *Agent) (*observedOutput, context.CancelFunc, <-chan error) {
	t.Helper()
	observationCtx, stopObserving := context.WithTimeout(t.Context(), time.Minute)
	// Observe shutdown after explicit cancellation, within the same deadline.
	ctx, cancel := context.WithCancel(observationCtx)
	output := &observedOutput{ctx: observationCtx, changed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, output, output) }()
	t.Cleanup(func() {
		defer stopObserving()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("agent shutdown: %v\n%s", err, output.text())
			}
		case <-time.After(15 * time.Second):
			t.Error("agent did not finish cancellation cleanup")
		}
	})
	output.await(t, "agent: started")
	return output, cancel, done
}

func TestRealBorgScheduledWorkflow(t *testing.T) {
	binary := realBorg(t)
	a, clock := newTestAgent(t, `  nightly:
    cron: "* * * * *"
    timezone: UTC
    stages:
      - tasks: [{type: backup, repository: local, archive_prefix: docs, sources: [payload]}]
      - tasks: [{type: prune, repository: local, archive_prefix: docs, keep_daily: 1}]
      - tasks: [{type: compact, repository: local}]
      - tasks: [{type: check, repository: local}]
      - tasks: [{type: check, repository: local, verify_data: true}]
      - tasks:
          - type: recovery_check
            repository: local
            archive_prefix: docs-
            paths: [payload]
            checks: [{kind: file_bytes, path: payload, bytes: "scheduled backup payload\n"}]
            sample: {live_root: ., limit: 1}
`, instant("2026-01-01T00:00:00Z"))
	base := a.selected.Directory
	repository := filepath.Join(base, "repo")
	const contents = "scheduled backup payload\n"
	if err := os.WriteFile(filepath.Join(base, "payload"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	borgCommand(t, binary, "init", "--encryption=none", repository)
	borgCommand(t, binary, "create", "--timestamp", "2020-01-01T00:00:00", repository+"::docs-old", filepath.Join(base, "payload"))
	output, _, _ := runObserved(t, a)
	// The agent uses the exact same workflow lock as manual invocations.
	identity, err := runlock.Config(a.selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manual, err := store.Workflow(identity, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	clock.set(instant("2026-01-01T00:01:00Z"))
	output.await(t, `workflow "nightly" skipped: already running`)
	manual.Close()
	clock.set(instant("2026-01-01T00:02:05Z"))
	output.await(t, `workflow "nightly" completed; tasks: 6 succeeded`)
	const archive = "docs-20260101T000205.000000000Z"
	listing := string(borgCommand(t, binary, "list", "--short", repository))
	if strings.TrimSpace(listing) != archive {
		t.Fatalf("scheduled backup/prune listing=%q\n%s", listing, output.text())
	}
	extracted := borgCommand(t, binary, "extract", "--stdout", repository+"::"+archive, "payload")
	if string(extracted) != contents {
		t.Fatalf("scheduled archive contents=%q", extracted)
	}
	if !strings.Contains(output.text(), "reclaiming eligible space") {
		t.Fatal("compact stage did not run:", output.text())
	}
	for _, text := range []string{"Recovery evidence v1", `Archive: "` + archive + `"`, "Cleanup: passed", "Sample: passed"} {
		if !strings.Contains(output.text(), text) {
			t.Errorf("missing scheduled recovery evidence %q:\n%s", text, output.text())
		}
	}
	for _, text := range []string{"nightly 4.1 check/local", "nightly 5.1 check/local", "Starting repository check", "Starting archive consistency check"} {
		if !strings.Contains(output.text(), text) {
			t.Errorf("missing scheduled check diagnostics %q:\n%s", text, output.text())
		}
	}
}

func holdRealRepository(t *testing.T, binary, repository string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	ready, release := filepath.Join(base, "ready"), filepath.Join(base, "release")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	done := make(chan error, 1)
	go func() {
		_, err := (borg.Runner{Executable: binary}).Run(ctx, borg.Request{
			Args: []string{"with-lock", "--lock-wait", "0", repository, self},
			Env: map[string]string{
				"CAIRN_AGENT_HOLDER": "1", "CAIRN_AGENT_READY": ready, "CAIRN_AGENT_RELEASE": release,
			},
		})
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("repository holder did not stop")
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("repository holder did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRealBorgScheduledCheckDamage(t *testing.T) {
	binary := realBorg(t)
	a, clock := newTestAgent(t, `  integrity:
    cron: "* * * * *"
    stages:
      - tasks: [{type: check, repository: local}]
      - tasks: [{type: compact, repository: local}]
`, instant("2026-01-01T00:00:00Z"))
	repository := filepath.Join(a.selected.Directory, "repo")
	borgCommand(t, binary, "init", "--encryption=none", repository)
	segments, err := filepath.Glob(filepath.Join(repository, "data", "*", "*"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("repository segments = %v: %v", segments, err)
	}
	segment := segments[0]
	damaged, err := os.ReadFile(segment)
	if err != nil || len(damaged) == 0 {
		t.Fatalf("read segment: %v", err)
	}
	damaged[0] ^= 0xff
	if err := os.WriteFile(segment, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	output, _, _ := runObserved(t, a)
	clock.set(instant("2026-01-01T00:01:00Z"))
	output.await(t, `workflow "integrity" completed with warnings; 1 stage(s) not started`)
	if !strings.Contains(output.text(), "Invalid segment magic") ||
		!strings.Contains(output.text(), "cairn [integrity 2.1 compact/local] skipped: previous stage completed with warnings") {
		t.Fatal("check warning diagnostics or stage barrier lost:", output.text())
	}
	after, err := os.ReadFile(segment)
	if err != nil || !bytes.Equal(damaged, after) {
		t.Fatalf("scheduled check modified damaged segment: %v", err)
	}
}

func TestRealBorgScheduledCancellation(t *testing.T) {
	for name, task := range map[string]string{
		"compact": "{type: compact, repository: local, lock_wait: 60}",
		"check":   "{type: check, repository: local, lock_wait: 60, verify_data: true}",
	} {
		t.Run(name, func(t *testing.T) { testRealBorgScheduledCancellation(t, task) })
	}
}

func testRealBorgScheduledCancellation(t *testing.T, task string) {
	t.Helper()
	binary := realBorg(t)
	a, clock := newTestAgent(t, `  nightly:
    cron: "* * * * *"
    stages: [{tasks: [`+task+`]}]
`, instant("2026-01-01T00:00:00Z"))
	repository := filepath.Join(a.selected.Directory, "repo")
	borgCommand(t, binary, "init", "--encryption=none", repository)
	holdRealRepository(t, binary, repository)
	output, cancel, _ := runObserved(t, a)
	clock.set(instant("2026-01-01T00:01:00Z"))
	deadline := time.Now().Add(15 * time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(repository, "lock.exclusive.*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled Borg did not wait for repository lock\n%s", output.text())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	output.await(t, "agent: stopped")
	identity, err := runlock.Config(a.selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(identity, "nightly")
	if err != nil {
		t.Fatal("workflow lock retained after cancellation cleanup:", err)
	}
	lock.Close()
	if !strings.Contains(output.text(), `workflow "nightly" interrupted`) {
		t.Fatal("missing cancellation result:", output.text())
	}
}
