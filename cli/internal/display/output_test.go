package display

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

type terminalBuffer struct {
	bytes.Buffer
	fd   uintptr
	file *os.File
}

func (b *terminalBuffer) Fd() uintptr { return b.fd }

func terminalOutput(t *testing.T) (*Output, *bytes.Buffer, *terminalBuffer) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "xterm-256color")
	stdout := &bytes.Buffer{}
	stderr := &terminalBuffer{fd: slave.Fd(), file: slave}
	return NewOutput(stdout, stderr), stdout, stderr
}

func taskEvent(run, workflow string, task int) lifecycle.Event {
	return lifecycle.Event{RunID: run, Workflow: workflow, Stage: 1, Task: task,
		Operation: "backup", Repository: "local", Kind: lifecycle.TaskStarted}
}

func TestBoardKeepsOneRowPerTaskAndPermanentMessages(t *testing.T) {
	output, stdout, stderr := terminalOutput(t)
	before, err := term.GetState(stderr.fd)
	if err != nil {
		t.Fatal(err)
	}
	first, second := taskEvent("one", "daily", 1), taskEvent("two", "other", 2)
	output.Observe(first)
	output.Observe(second)
	id := TaskID{first.RunID, first.Stage, first.Task}
	output.Progress(id, Attribution(first), borg.Progress{Message: "checking 25%"})
	start := stderr.Len()
	n, err := fmt.Fprintln(output.Writer(false, Label("borg", Attribution(second))), "warning first\nwarning second")
	if err != nil || n != len("warning first\nwarning second\n") {
		t.Fatalf("write=%d %v", n, err)
	}
	last := stderr.String()[start:]
	for _, text := range []string{
		"\x1b[2A\r\x1b[J",
		"borg [other 1.2 backup/local] warning first\nborg [other 1.2 backup/local] warning second\n",
		"cairn [daily 1.1 backup/local] running | borg: checking 25%",
		"cairn [other 1.2 backup/local] running",
	} {
		if !strings.Contains(last, text) {
			t.Fatalf("missing %q in %q", text, last)
		}
	}
	if output.rows != 2 || len(output.active) != 2 {
		t.Fatalf("rows=%d active=%d", output.rows, len(output.active))
	}
	if _, err := fmt.Fprintln(output.Writer(true, ""), "operation result"); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "operation result\n" {
		t.Fatalf("board polluted stdout: %q", stdout.String())
	}
	first.Kind, second.Kind = lifecycle.TaskFinished, lifecycle.TaskFinished
	output.Observe(first)
	output.Observe(second)
	if output.rows != 0 || len(output.active) != 0 || !strings.HasSuffix(stderr.String(), "\x1b[1A\r\x1b[J") {
		t.Fatalf("board not removed: %q", stderr.String())
	}
	after, err := term.GetState(stderr.fd)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal modes changed: %v", err)
	}
}

func TestPlainLogsHaveNoBoardAndRetainProgress(t *testing.T) {
	var stdout, stderr bytes.Buffer
	output := NewOutput(&stdout, &stderr)
	event := taskEvent("run", "daily", 2)
	output.Observe(event)
	for _, message := range []string{"checking 25%", "checking 50%"} {
		if err := output.Progress(TaskID{"run", 1, 2}, Attribution(event), borg.Progress{Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	output.Progress(TaskID{"run", 1, 2}, Attribution(event), borg.Progress{Finished: true})
	event.Kind = lifecycle.TaskFinished
	output.Observe(event)
	want := "borg [daily 1.2 backup/local] checking 25%\nborg [daily 1.2 backup/local] checking 50%\n"
	if stderr.String() != want || stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestConcurrentSplitDiagnosticsKeepAttribution(t *testing.T) {
	var buffer bytes.Buffer
	output := NewOutput(&buffer, &buffer)
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Go(func() {
			writer := NewLines(output.Writer(false, fmt.Sprintf("borg [task-%d] ", i)))
			for _, chunk := range []string{"fir", "st\nsecond\n", "unter", "minated"} {
				if _, err := io.WriteString(writer, chunk); err != nil {
					t.Error(err)
				}
			}
			if err := writer.Flush(); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	var count int
	for _, line := range strings.Split(strings.TrimSuffix(buffer.String(), "\n"), "\n") {
		var task int
		if _, err := fmt.Sscanf(line, "borg [task-%d]", &task); err != nil {
			t.Fatalf("unattributed diagnostic: %q", line)
		}
		prefix := fmt.Sprintf("borg [task-%d] ", task)
		switch strings.TrimPrefix(line, prefix) {
		case "first", "second", "unterminated":
		default:
			t.Fatalf("mixed diagnostic: %q", line)
		}
		count++
	}
	if count != 60 {
		t.Fatalf("lines=%d", count)
	}
}

func TestSmallTerminalFallsBackAndBoardClipsUntrustedProgress(t *testing.T) {
	output, _, stderr := terminalOutput(t)
	if err := pty.Setsize(stderr.file, &pty.Winsize{Rows: 4, Cols: 40}); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		output.Observe(taskEvent(fmt.Sprint(i), "daily", i+1))
	}
	if output.rows != 3 || !strings.Contains(stderr.String(), "4 more active tasks") {
		t.Fatalf("oversized board: rows=%d %q", output.rows, stderr.String())
	}
	if err := pty.Setsize(stderr.file, &pty.Winsize{Rows: 2, Cols: 10}); err != nil {
		t.Fatal(err)
	}
	output.Progress(TaskID{"0", 1, 1}, "daily 1.1 backup/local", borg.Progress{Message: "safe\x1b[2J\nsecond"})
	if !strings.Contains(stderr.String(), "borg [daily 1.1 backup/local] safe\\x1b[2J\nborg [daily 1.1 backup/local] second\n") {
		t.Fatalf("unsafe or dropped progress: %q", stderr.String())
	}
}

func TestDumbTerminalUsesPlainLogs(t *testing.T) {
	_, _, stderr := terminalOutput(t)
	t.Setenv("TERM", "dumb")
	output := NewOutput(io.Discard, stderr)
	output.Observe(taskEvent("run", "daily", 1))
	output.Progress(TaskID{"run", 1, 1}, "daily", borg.Progress{Message: "checking"})
	if stderr.String() != "borg [daily] checking\n" {
		t.Fatalf("dumb terminal got board: %q", stderr.String())
	}
}

func TestInvocationWarningsShareBoardOutput(t *testing.T) {
	output, _, stderr := terminalOutput(t)
	ctx, warnings := WithDiagnosticSink(context.Background(), stderr)
	if _, err := fmt.Fprintln(warnings, "before execution"); err != nil {
		t.Fatal(err)
	}
	AttachDiagnostics(ctx, output)
	event := taskEvent("run", "daily", 1)
	output.Observe(event)
	start := stderr.Len()
	if _, err := fmt.Fprintln(warnings, "Warning: history unavailable"); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[1A\r\x1b[JWarning: history unavailable\ncairn [daily 1.1 backup/local] running\n"
	if stderr.String()[start:] != want {
		t.Fatalf("warning bypassed board: %q", stderr.String()[start:])
	}
	event.Kind = lifecycle.TaskFinished
	output.Observe(event)
	if _, err := fmt.Fprintln(warnings, "after execution"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stderr.String(), "after execution\n") {
		t.Fatal("shutdown warning lost")
	}
}
