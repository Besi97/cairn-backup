package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
		run.Emit(lifecycle.Event{Kind: lifecycle.RunFinished, Outcome: lifecycle.Success})
		return workflow.Result{Outcome: borg.Success}, nil
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
	for !strings.Contains(body, `cairn_workflow_events_total{workflow="scheduled",outcome="success"} 1`) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		body = fetch()
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatal(err)
	}
	if !strings.Contains(body, `cairn_workflow_events_total{workflow="scheduled",outcome="success"} 1`) {
		t.Fatalf("scheduled event missing: %s", body)
	}
	client.CloseIdleConnections()
	if resp, err := client.Get("http://" + address + "/metrics"); err == nil {
		resp.Body.Close()
		t.Fatal("listener survived shutdown")
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
