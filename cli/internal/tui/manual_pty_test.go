package tui

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
	"github.com/creack/pty"
)

func TestManualProgressTerminalNavigation(t *testing.T) {
	m := manualModel(t)
	started, release := make(chan struct{}), make(chan struct{})
	m.runs.run = func(ctx context.Context, _ workflow.Manual) (workflow.Result, error) {
		close(started)
		select {
		case <-release:
			return workflow.Result{Outcome: borg.Success}, nil
		case <-ctx.Done():
			return workflow.Result{Outcome: borg.Interrupted}, ctx.Err()
		}
	}
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	chunks := make(chan string, 100)
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
	done := make(chan error, 1)
	go func() {
		_, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(slave), tea.WithOutput(slave), tea.WithoutSignalHandler()).Run()
		m.runs.close()
		done <- err
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("terminal did not stop")
		}
	}()
	wait := func(text string) {
		t.Helper()
		output := ""
		for !strings.Contains(output, text) {
			select {
			case chunk := <-chunks:
				output += chunk
			case <-ctx.Done():
				t.Fatalf("terminal did not show %q: %q", text, output)
			}
		}
	}
	send := func(keys string) {
		t.Helper()
		if _, err := io.WriteString(master, keys); err != nil {
			t.Fatal(err)
		}
	}
	wait("Workflows")
	send("s")
	wait("Confirm workflow")
	send("y")
	<-started
	wait("Workflow progress")
	send("\x1b")
	wait("s start")
	send("p")
	wait("Latest update")
	close(release)
	wait("SUCCESS")
	send("q")
}
