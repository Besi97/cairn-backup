package agent

import (
	"context"
	"io"
	"sync"
)

// One mutex serializes both streams across ALL workflows and agent reports,
// even when stdout and stderr refer to the same non-concurrent writer.
type streams struct {
	mu             sync.Mutex
	stdout, stderr io.Writer
	firstErr       error
	cancel         context.CancelFunc
}

type stream struct {
	output *streams
	stderr bool
}

func (w stream) Write(p []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.firstErr != nil {
		return 0, o.firstErr
	}
	destination := o.stdout
	if w.stderr {
		destination = o.stderr
	}
	if destination == nil {
		return len(p), nil
	}
	n, err := destination.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.firstErr = err
		o.cancel()
	}
	return n, err
}

func (o *streams) err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.firstErr
}
