package display

import (
	"bytes"
	"io"
	"sync"
)

// Lines keeps split diagnostic writes together until a complete line is ready.
type Lines struct {
	mu      sync.Mutex
	writer  io.Writer
	pending []byte
	err     error
}

func NewLines(writer io.Writer) *Lines { return &Lines{writer: writer} }

func (w *Lines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	size := len(p)
	for len(p) > 0 {
		limit := min(len(p), 1024*1024-len(w.pending))
		line, _, found := bytes.Cut(p[:limit], []byte{'\n'})
		w.pending = append(w.pending, line...)
		consumed := len(line)
		if found {
			consumed++
		}
		if found || len(w.pending) >= 1024*1024 {
			w.flush()
		}
		p = p[consumed:]
	}
	if w.err != nil {
		return 0, w.err
	}
	return size, nil
}

func (w *Lines) flush() {
	if w.err == nil {
		w.pending = append(w.pending, '\n')
		n, err := w.writer.Write(w.pending)
		if err == nil && n != len(w.pending) {
			err = io.ErrShortWrite
		}
		w.err = err
	}
	w.pending = w.pending[:0]
}

func (w *Lines) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.flush()
	}
	return w.err
}
