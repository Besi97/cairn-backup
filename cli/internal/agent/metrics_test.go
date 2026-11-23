package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

func TestMetricsAgentLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	selected := loadTestConfig(t, definition("scheduled", "* * * * *", "UTC")+definition("manual", "", ""))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	selected.Metrics = &config.Metrics{Listen: address}
	identity, err := runlock.Config(selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Observe(lifecycle.Event{RunID: "manual", ConfigID: identity.Digest(), Workflow: "manual", Source: lifecycle.Manual, Sequence: 1, Time: time.Now(), Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	a, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: instant("2026-01-01T00:00:00Z")}
	a.clock = clock
	a.execute = func(ctx context.Context, plan workflow.Plan) (workflow.Result, error) {
		run := lifecycle.New(ctx, lifecycle.Event{Workflow: plan.Name})
		run.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
		run.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Failure})
		return workflow.Result{Outcome: borg.Failure}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, io.Discard, io.Discard) }()
	client := &http.Client{Timeout: time.Second}
	fetch := func() string {
		resp, err := client.Get("http://" + address + "/metrics")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return string(data)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fetch() == "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	body := fetch()
	if !strings.Contains(body, `cairn_workflow_last_success_known{workflow="manual"} 1`) {
		cancel()
		<-done
		t.Fatalf("manual history missing: %s", body)
	}
	clock.set(instant("2026-01-01T00:01:00Z"))
	for !strings.Contains(body, `cairn_workflow_events_total{workflow="scheduled",outcome="failure"} 1`) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		body = fetch()
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	if !strings.Contains(body, `cairn_workflow_events_total{workflow="scheduled",outcome="failure"} 1`) ||
		!strings.Contains(body, `cairn_workflow_last_outcome{workflow="scheduled",outcome="failure"} 1`) ||
		!strings.Contains(body, `cairn_workflow_last_success_known{workflow="scheduled"} 0`) {
		t.Fatalf("scheduled event missing: %s", body)
	}
	client.CloseIdleConnections()
	if resp, err := client.Get("http://" + address + "/metrics"); err == nil {
		resp.Body.Close()
		t.Fatal("listener survived shutdown")
	}
}

func TestMetricsRestartAndScheduledContention(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	selected := loadTestConfig(t, definition("nightly", "* * * * *", "UTC"))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selected.Metrics = &config.Metrics{Listen: l.Addr().String()}
	l.Close()
	store, err := history.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	a, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: instant("2026-01-01T00:00:00Z")}
	a.clock = clock
	var calls atomic.Int32
	release := make(chan struct{})
	a.execute = func(ctx context.Context, plan workflow.Plan) (workflow.Result, error) {
		run := lifecycle.New(lifecycle.WithAdditionalObserver(ctx, store.Observe), lifecycle.Event{Workflow: plan.Name})
		run.Emit(lifecycle.Event{Kind: lifecycle.RunStarted})
		if calls.Add(1) == 2 {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		run.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
		return workflow.Result{Outcome: borg.Success}, nil
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	scrape := func() string {
		resp, err := client.Get("http://" + selected.Metrics.Listen + "/metrics")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	wait := func(want string) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var body string
		for time.Now().Before(deadline) {
			body = scrape()
			if strings.Contains(body, want) {
				return body
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q in scrape:\n%s", want, body)
		return ""
	}
	start := func(agent *Agent) (context.CancelFunc, <-chan error) {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- agent.Run(ctx, io.Discard, io.Discard) }()
		return cancel, done
	}
	cancel, done := start(a)
	defer cancel()
	body := wait(`cairn_workflow_last_success_known{workflow="nightly"} 0`)
	if strings.Contains(body, `cairn_workflow_last_success_timestamp_seconds{workflow="nightly"}`) {
		t.Fatal("unknown success has a timestamp")
	}
	clock.set(instant("2026-01-01T00:01:00Z"))
	wait(`cairn_workflow_events_total{workflow="nightly",outcome="success"} 1`)
	clock.set(instant("2026-01-01T00:02:00Z"))
	wait(`cairn_workflow_in_flight{workflow="nightly"} 1`)
	clock.set(instant("2026-01-01T00:03:00Z"))
	body = wait(`cairn_workflow_busy_total{workflow="nightly"} 1`)
	for _, want := range []string{
		`cairn_workflow_events_total{workflow="nightly",outcome="skipped"} 1`,
		`cairn_workflow_last_outcome{workflow="nightly",outcome="skipped"} 1`,
		`cairn_workflow_last_success_known{workflow="nightly"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
	close(release)
	wait(`cairn_workflow_events_total{workflow="nightly",outcome="success"} 2`)
	if calls.Load() != 2 {
		t.Fatalf("unexpected queued dispatch: %d", calls.Load())
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}

	restarted, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	restarted.clock = clock
	cancel, done = start(restarted)
	defer cancel()
	body = wait(`cairn_workflow_last_success_known{workflow="nightly"} 1`)
	for _, want := range []string{
		`cairn_history_available 1`,
		`cairn_workflow_last_success_timestamp_seconds{workflow="nightly"}`,
		`cairn_workflow_events_total{workflow="nightly",outcome="success"} 0`,
		`cairn_workflow_events_total{workflow="nightly",outcome="skipped"} 0`,
		`cairn_workflow_busy_total{workflow="nightly"} 0`,
		`cairn_workflow_in_flight{workflow="nightly"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q after restart:\n%s", want, body)
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
}
func TestMetricsBindFailureDoesNotStopAgent(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	selected := loadTestConfig(t, definition("scheduled", "* * * * *", "UTC"))
	selected.Metrics = &config.Metrics{Listen: l.Addr().String()}
	a, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	err = a.Run(ctx, io.Discard, &output)
	if err != context.DeadlineExceeded || !strings.Contains(output.String(), "metrics listener failed") || !strings.Contains(output.String(), "started; 1 scheduled") {
		t.Fatal(fmt.Sprintf("%v: %s", err, output.String()))
	}
}

func TestMetricsWorkflowLockContention(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	selected := loadTestConfig(t, definition("nightly", "* * * * *", "UTC"))
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	selected.Metrics = &config.Metrics{Listen: l.Addr().String()}
	l.Close()
	id, err := runlock.Config(selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(id, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	a, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: instant("2026-01-01T00:00:00Z")}
	a.clock = clock
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, io.Discard, io.Discard) }()
	defer func() {
		cancel()
		if err := <-done; err != context.Canceled {
			t.Errorf("shutdown: %v", err)
		}
	}()
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	wait := func(want string) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var body string
		for time.Now().Before(deadline) {
			resp, err := client.Get("http://" + selected.Metrics.Listen + "/metrics")
			if err == nil {
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				body = string(data)
				if strings.Contains(body, want) {
					return body
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q:\n%s", want, body)
		return ""
	}
	wait(`cairn_workflow_last_success_known{workflow="nightly"} 0`)
	clock.set(instant("2026-01-01T00:01:00Z"))
	body := wait(`cairn_workflow_busy_total{workflow="nightly"} 1`)
	for _, want := range []string{
		`cairn_workflow_events_total{workflow="nightly",outcome="skipped"} 1`,
		`cairn_workflow_last_outcome{workflow="nightly",outcome="skipped"} 1`,
		`cairn_workflow_in_flight{workflow="nightly"} 0`,
		`cairn_workflow_last_success_known{workflow="nightly"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
}
