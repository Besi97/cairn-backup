// Package display coordinates human logs and a transient task board.
package display

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type TaskID struct {
	Run         string
	Stage, Task int
}

type activeTask struct {
	label    string
	progress string
}

// Output owns both streams, even when they share a terminal or a non-concurrent
// writer. The board uses stderr only and never reads input or changes tty modes.
type Output struct {
	mu             sync.Mutex
	stdout, stderr io.Writer
	outErr, errErr error
	fd             uintptr
	terminal       bool
	rows           int
	active         map[TaskID]activeTask
	diagnostics    *labelledWriter
}

func NewOutput(stdout, stderr io.Writer) *Output {
	o := &Output{stdout: stdout, stderr: stderr, active: make(map[TaskID]activeTask)}
	o.diagnostics = o.Writer(false, "")
	if fd, ok := stderr.(interface{ Fd() uintptr }); ok {
		o.fd = fd.Fd()
		o.terminal = term.IsTerminal(o.fd) && os.Getenv("TERM") != "dumb"
	}
	return o
}

func Attribution(event lifecycle.Event) string {
	if event.Workflow == "" {
		return event.Operation
	}
	operation := safe(event.Operation)
	if event.Repository != "" {
		operation += "/" + safe(event.Repository)
	}
	return fmt.Sprintf("%s %d.%d %s", safe(event.Workflow),
		event.Stage, event.Task, operation)
}

func Label(source, attribution string) string {
	if attribution == "" {
		return source + ": "
	}
	return source + " [" + attribution + "] "
}

func (o *Output) Writer(stdout bool, label string) *labelledWriter {
	return &labelledWriter{output: o, stdout: stdout, label: label}
}

// Write is the scheduler's diagnostic stream.
func (o *Output) Write(p []byte) (int, error) {
	return o.diagnostics.Write(p)
}

type labelledWriter struct {
	output  *Output
	stdout  bool
	label   string
	pending []byte
}

func (w *labelledWriter) Write(p []byte) (int, error) {
	size := len(p)
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	o.clear()
	destination, sticky := o.stderr, &o.errErr
	if w.stdout {
		destination, sticky = o.stdout, &o.outErr
	}
	if *sticky != nil {
		return 0, *sticky
	}
	p = append(w.pending, p...)
	w.pending = nil
	for i := 0; i < len(p); {
		if !utf8.FullRune(p[i:]) {
			w.pending = append(w.pending, p[i:]...)
			p = p[:i]
			break
		}
		_, width := utf8.DecodeRune(p[i:])
		i += width
	}
	if len(p) > 0 {
		var framed bytes.Buffer
		for len(p) > 0 {
			line, rest, _ := bytes.Cut(p, []byte{'\n'})
			framed.WriteString(w.label)
			framed.WriteString(safe(string(line)))
			framed.WriteByte('\n')
			p = rest
		}
		write(destination, framed.Bytes(), sticky)
	}
	o.draw()
	if *sticky != nil {
		return 0, *sticky
	}
	// Framing changes the destination size, not the caller's write count.
	return size, nil
}

// Flush emits an incomplete UTF-8 suffix as replacement characters at EOF.
func (w *labelledWriter) Flush() error {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(w.pending) == 0 {
		return nil
	}
	o.clear()
	destination, sticky := o.stderr, &o.errErr
	if w.stdout {
		destination, sticky = o.stdout, &o.outErr
	}
	text := w.label + safe(string(w.pending)) + "\n"
	w.pending = nil
	write(destination, []byte(text), sticky)
	o.draw()
	return *sticky
}

func write(w io.Writer, p []byte, sticky *error) {
	if *sticky != nil || w == nil {
		return
	}
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	*sticky = err
}

func (o *Output) Observe(event lifecycle.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := TaskID{event.RunID, event.Stage, event.Task}
	switch event.Kind {
	case lifecycle.TaskStarted:
		o.clear()
		o.active[id] = activeTask{label: Attribution(event)}
	case lifecycle.TaskFinished, lifecycle.TaskSkipped:
		o.clear()
		delete(o.active, id)
	case lifecycle.RunFinished, lifecycle.RunSkipped:
		o.clear()
		for id := range o.active {
			if id.Run == event.RunID {
				delete(o.active, id)
			}
		}
	default:
		return nil
	}
	o.draw()
	return nil
}

func (o *Output) Progress(id TaskID, attribution string, progress borg.Progress) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if task, ok := o.active[id]; ok && o.boardSize() > 0 {
		o.clear()
		task.progress = ""
		if !progress.Finished {
			task.progress = safe(progress.Message)
		}
		o.active[id] = task
		o.draw()
	} else if !progress.Finished && progress.Message != "" {
		o.clear()
		for _, line := range strings.Split(progress.Message, "\n") {
			text := Label("borg", attribution) + safe(line) + "\n"
			write(o.stderr, []byte(text), &o.errErr)
		}
	}
	return o.errErr
}

func (o *Output) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return errors.Join(o.outErr, o.errErr)
}

func (o *Output) FinishTask(id TaskID) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.active[id]; ok {
		o.clear()
		delete(o.active, id)
		o.draw()
	}
}

func (o *Output) clear() {
	if o.rows == 0 {
		return
	}
	text := fmt.Sprintf("\x1b[%dA\r\x1b[J", o.rows)
	write(o.stderr, []byte(text), &o.errErr)
	o.rows = 0
}

func (o *Output) boardSize() int {
	if !o.terminal {
		return 0
	}
	width, height, err := term.GetSize(o.fd)
	if err != nil || width < 24 || height < 3 {
		return 0
	}
	return height - 1
}

func (o *Output) draw() {
	capacity := o.boardSize()
	if capacity == 0 || len(o.active) == 0 || o.errErr != nil {
		return
	}
	width, _, _ := term.GetSize(o.fd)
	var tasks []activeTask
	for _, task := range o.active {
		tasks = append(tasks, task)
	}
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].label < tasks[j].label })
	var content strings.Builder
	visible := min(len(tasks), capacity)
	if len(tasks) > capacity {
		visible--
	}
	for _, task := range tasks[:visible] {
		line := Label("cairn", task.label) + "running"
		if task.progress != "" {
			line += " | borg: " + task.progress
		}
		content.WriteString(ansi.Truncate(line, width-1, "…"))
		content.WriteString("\n")
	}
	if len(tasks) > visible {
		content.WriteString(ansi.Truncate(fmt.Sprintf("cairn: %d more active tasks", len(tasks)-visible), width-1, "…"))
		content.WriteString("\n")
		visible++
	}
	write(o.stderr, []byte(content.String()), &o.errErr)
	o.rows = visible
}

func safe(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
