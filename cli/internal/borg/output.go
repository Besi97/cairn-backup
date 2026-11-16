package borg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Progress is Borg's explicit progress channel, separate from diagnostics and
// operation results. Message is human text; Finished clears a live indicator.
type Progress struct {
	Message  string
	Finished bool
}

type logRecord struct {
	Type         string  `json:"type"`
	Message      *string `json:"message"`
	Finished     bool    `json:"finished"`
	Original     *int64  `json:"original_size"`
	Compressed   *int64  `json:"compressed_size"`
	Deduplicated *int64  `json:"deduplicated_size"`
	Files        *int64  `json:"nfiles"`
	Path         string  `json:"path"`
	Status       *string `json:"status"`
}

// JSON stderr and ordinary stdout have separate framing buffers. In particular,
// a partial stdout diagnostic must never be mistaken for a JSON stderr record.
type logOutput struct {
	mu       sync.Mutex
	writer   io.Writer
	progress func(Progress) error
	raw      bool
	pending  []byte
	afterCR  bool
	err      error
}

const maxLogRecord = 1024 * 1024

func (w *logOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	for _, b := range p {
		if b == '\n' && w.afterCR {
			w.afterCR = false
			continue
		}
		if b == '\n' || b == '\r' {
			w.record(w.pending)
			w.pending = w.pending[:0]
			w.afterCR = b == '\r'
		} else {
			w.afterCR = false
			w.pending = append(w.pending, b)
			if len(w.pending) >= maxLogRecord {
				// Keep malformed or oversized output visible without retaining
				// an unbounded line or treating fragments as progress.
				w.text(w.pending)
				w.pending = w.pending[:0]
			}
		}
		if w.err != nil {
			return 0, w.err
		}
	}
	return len(p), nil
}

func (w *logOutput) record(line []byte) {
	if w.raw {
		w.text(line)
		return
	}
	var record logRecord
	if err := json.Unmarshal(line, &record); err != nil || record.Type == "" {
		w.text(line)
		return
	}
	switch record.Type {
	case "file_status":
		if record.Status != nil {
			w.text([]byte(*record.Status + " " + record.Path))
			return
		}
	case "progress_message", "progress_percent":
		if record.Message != nil || record.Finished {
			message := ""
			if record.Message != nil {
				message = *record.Message
			}
			w.err = w.progress(Progress{Message: message, Finished: record.Finished})
			return
		}
	case "archive_progress":
		if record.Finished {
			w.err = w.progress(Progress{Finished: true})
			return
		}
		if record.Files != nil && record.Original != nil && record.Compressed != nil && record.Deduplicated != nil {
			message := fmt.Sprintf("%s O %s C %s D %d N %s",
				progressSize(*record.Original), progressSize(*record.Compressed),
				progressSize(*record.Deduplicated), *record.Files, record.Path)
			w.err = w.progress(Progress{Message: strings.TrimSpace(message)})
			return
		}
	}
	if record.Message != nil {
		w.text([]byte(*record.Message))
	} else {
		w.text(line)
	}
}

func progressSize(n int64) string {
	value := float64(n)
	for _, unit := range []string{"B", "kB", "MB", "GB", "TB", "PB", "EB"} {
		if value < 1000 || unit == "EB" {
			if unit == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.2f %s", value, unit)
		}
		value /= 1000
	}
	return ""
}

func (w *logOutput) text(p []byte) {
	if w.err != nil {
		return
	}
	// Preserve every line and printable character, but never let a forwarded
	// diagnostic move the cursor or impersonate the live status region.
	lines := bytes.Split(p, []byte{'\n'})
	for _, line := range lines {
		var text strings.Builder
		for _, r := range string(line) {
			switch {
			case r == '\t':
				text.WriteRune(r)
			case unicode.IsControl(r) || r == '\u2028' || r == '\u2029':
				quoted := strconv.QuoteRune(r)
				text.WriteString(quoted[1 : len(quoted)-1])
			default:
				text.WriteRune(r)
			}
		}
		text.WriteByte('\n')
		n, err := io.WriteString(w.writer, text.String())
		if err == nil && n != text.Len() {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			return
		}
	}
}

func (w *logOutput) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 && w.err == nil {
		w.record(w.pending)
		w.pending = nil
	}
	return w.err
}
