package display

import (
	"context"
	"io"
	"sync"
)

type diagnosticsKey struct{}

type diagnosticSink struct {
	mu     sync.Mutex
	writer io.Writer
}

// WithDiagnosticSink lets invocation-level warnings share the active renderer
// without hiding the original terminal files from CLI/TUI detection.
func WithDiagnosticSink(ctx context.Context, fallback io.Writer) (context.Context, io.Writer) {
	sink := &diagnosticSink{writer: fallback}
	return context.WithValue(ctx, diagnosticsKey{}, sink), sink
}

func AttachDiagnostics(ctx context.Context, output *Output) {
	if sink, ok := ctx.Value(diagnosticsKey{}).(*diagnosticSink); ok {
		sink.mu.Lock()
		sink.writer = output
		sink.mu.Unlock()
	}
}

func (s *diagnosticSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	writer := s.writer
	s.mu.Unlock()
	return writer.Write(p)
}
