package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

func manualModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("config"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := "secret-passphrase"
	selected := &config.Config{
		Path: path, Directory: dir,
		Repositories: map[string]config.Repository{"repo": {Location: "/repo", Passphrase: &secret}},
		Workflows: map[string]config.Workflow{
			"alpha": {Stages: []config.Stage{{Tasks: []config.Task{{Type: "compact", Repository: "repo"}}}}},
			"zeta": {Stages: []config.Stage{
				{Tasks: []config.Task{{Type: "prune", Repository: "repo", ArchivePrefix: "daily", KeepDaily: 7}}},
				{Tasks: []config.Task{{Type: "compact", Repository: "repo"}}},
			}},
		},
	}
	m := New(selected)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m = next.(Model)
	m.page = workflowsPage
	return m
}

func TestManualConfirmationAndCancellation(t *testing.T) {
	m := manualModel(t)
	started := make(chan string, 1)
	release := make(chan struct{})
	m.runs.run = func(ctx context.Context, manual workflow.Manual) (workflow.Result, error) {
		started <- manual.Plan.Name
		<-ctx.Done()
		<-release
		return workflow.Result{Outcome: borg.Interrupted}, ctx.Err()
	}
	m, _ = press(m, tea.KeyDown, 0)
	m, cmd := press(m, 's', 0)
	if cmd != nil || m.page != confirmPage {
		t.Fatal("start did not open confirmation")
	}
	text := m.confirmation()
	for _, want := range []string{"zeta", "Stage 1", "prune", "Stage 2", "compact", "repo"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "secret-passphrase") {
		t.Fatal("confirmation exposed secret")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	select {
	case <-started:
		t.Fatal("cancelled confirmation started work")
	default:
	}
	m, _ = press(m, 's', 0)
	m, cmd = press(m, 'y', 0)
	if cmd == nil || !m.runs.active || <-started != "zeta" {
		t.Fatal("confirmation did not start selected workflow")
	}
	m, other := press(m, 's', 0)
	if other != nil || m.page == confirmPage {
		t.Fatal("started a second run")
	}
	m, other = press(m, 'q', 0)
	if other != nil || !m.exiting {
		t.Fatal("quit did not wait")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-done:
		t.Fatal("quit detached before work stopped")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	msg := <-done
	next, quit := m.Update(msg)
	if !next.(Model).exiting {
		t.Fatal("lost exit state")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("did not quit after run stopped")
	}
}

func TestManualContentionUsesSharedLock(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := manualModel(t)
	id, err := runlock.Config(m.selected.Path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runlock.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	lock, err := store.Workflow(id, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	events := make(chan lifecycle.Event, 20)
	m.runs.ctx = lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events <- event
		return nil
	})
	m, _ = press(m, 's', 0)
	m, cmd := press(m, 'y', 0)
	msg := cmd().(manualDone)
	if !strings.Contains(msg.message, "already running") {
		t.Fatalf("contention: %s", msg.message)
	}
	m.runs.close()
	close(events)
	found := false
	for event := range events {
		if event.Kind == lifecycle.RunSkipped && event.Reason == lifecycle.Busy {
			found = true
		}
	}
	if !found {
		t.Fatal("busy attempt did not emit lifecycle history event")
	}
}

func TestManualInterruptAndExternalShutdownWait(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "ctrl-c", true: "program shutdown"}[external], func(t *testing.T) {
			m := manualModel(t)
			started, stopped := make(chan struct{}), make(chan struct{})
			m.runs.run = func(ctx context.Context, _ workflow.Manual) (workflow.Result, error) {
				close(started)
				<-ctx.Done()
				<-stopped
				return workflow.Result{Outcome: borg.Interrupted}, ctx.Err()
			}
			m, _ = press(m, 's', 0)
			m, cmd := press(m, 'y', 0)
			<-started
			wait := make(chan struct{})
			if external {
				go func() { m.runs.close(); close(wait) }()
			} else {
				var quit tea.Cmd
				m, quit = press(m, 'c', tea.ModCtrl)
				if quit != nil || !m.exiting || !m.interrupted {
					t.Fatal("interrupt did not wait")
				}
				go func() { msg := cmd(); _, quit = m.Update(msg); close(wait) }()
				defer func() {
					if _, ok := quit().(tea.InterruptMsg); !ok {
						t.Error("did not preserve interrupt")
					}
				}()
			}
			select {
			case <-wait:
				t.Fatal("returned before workflow stopped")
			case <-time.After(30 * time.Millisecond):
			}
			close(stopped)
			<-wait
		})
	}
}
