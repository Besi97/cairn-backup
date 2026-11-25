package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

type failMatchingWriter struct {
	dst   io.Writer
	match string
}

func (w failMatchingWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.match) {
		return 0, errors.New("selected output failure")
	}
	return w.dst.Write(p)
}

func TestCancellationWithOutputFailure(t *testing.T) {
	for _, stream := range []string{"summary", "flush"} {
		t.Run(stream, func(t *testing.T) {
			fakeBorgEnvironment(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			base := filepath.Join(t.TempDir(), "ready")
			t.Setenv("FAKE_TREE", base)
			if stream == "flush" {
				t.Setenv("FAKE_SIGNAL_PARTIAL_UTF8", "yes")
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			var out, diagnostic io.Writer = &stdout, &stderr
			if stream == "summary" {
				out = failMatchingWriter{&stdout, "interrupted"}
			} else {
				diagnostic = failMatchingWriter{&stderr, "�"}
			}
			done := make(chan int, 1)
			go func() { done <- Run(ctx, []string{"compact", "--repository=repo"}, out, diagnostic, "test") }()
			awaitFile(t, base+".parent")
			awaitFile(t, base+".child")
			cancel(cancellation.Signal(syscall.SIGTERM))
			code := <-done
			if code != 143 || !strings.Contains(stderr.String(), "Interrupted:") || !strings.Contains(stderr.String(), "selected output failure") {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		code    int
		wantOut string
		wantErr string
	}{
		{name: "no arguments", wantOut: "Usage:"},
		{name: "help", args: []string{"--help"}, wantOut: "Usage:"},
		{name: "short help", args: []string{"-h"}, wantOut: "Usage:"},
		{name: "version", args: []string{"--version"}, wantOut: "cairn 0.1.0-test\n"},
		{name: "unknown flag", args: []string{"--unknown"}, code: 3, wantErr: "unknown flag: --unknown"},
		{name: "unknown command", args: []string{"missing"}, code: 3, wantErr: "unknown command"},
		{name: "backup needs inputs", args: []string{"backup"}, code: 3, wantErr: "exactly one"},
		{name: "backup help", args: []string{"backup", "--help"}, wantOut: "--repository"},
		{name: "prune needs inputs", args: []string{"prune"}, code: 3, wantErr: "repository"},
		{name: "prune help", args: []string{"prune", "--help"}, wantOut: "--keep-within"},
		{name: "compact needs inputs", args: []string{"compact"}, code: 3, wantErr: "repository"},
		{name: "compact help", args: []string{"compact", "--help"}, wantOut: "--repository"},
		{name: "positional after separator", args: []string{"--", "source"}, code: 3, wantErr: "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if got := Run(t.Context(), tt.args, &stdout, &stderr, "0.1.0-test"); got != tt.code {
				t.Fatalf("exit code = %d, want %d; stderr: %s", got, tt.code, &stderr)
			}
			if tt.wantOut == "" {
				if stdout.Len() != 0 {
					t.Errorf("unexpected stdout: %q", &stdout)
				}
			} else if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want %q", &stdout, tt.wantOut)
			}
			if tt.wantErr == "" {
				if stderr.Len() != 0 {
					t.Errorf("unexpected stderr: %q", &stderr)
				}
			} else {
				if !strings.Contains(stderr.String(), tt.wantErr) {
					t.Errorf("stderr = %q, want %q", &stderr, tt.wantErr)
				}
				if strings.Count(stderr.String(), "Error:") != 1 {
					t.Errorf("expected one error diagnostic: %q", &stderr)
				}
			}
		})
	}
}

type cancelOnText struct {
	dst    io.Writer
	text   string
	cancel context.CancelFunc
}

func (w cancelOnText) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.text) {
		w.cancel()
	}
	return w.dst.Write(p)
}

func TestSingleOperationCancellationDuringCleanup(t *testing.T) {
	fakeBorgEnvironment(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, tc := range []struct {
		exit string
		code int
	}{{"0", 0}, {"1", 1}, {"2", 2}, {"130", 130}} {
		t.Run(tc.exit, func(t *testing.T) {
			t.Setenv("FAKE_TREE", filepath.Join(t.TempDir(), "ready"))
			t.Setenv("FAKE_PARENT_EXIT", "yes")
			t.Setenv("FAKE_SIGNAL_OUTPUT", "yes")
			t.Setenv("FAKE_EXIT", tc.exit)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			writer := cancelOnText{&stderr, "child stopped", func() { cancel(cancellation.Signal(syscall.SIGTERM)) }}
			code := Run(ctx, []string{"compact", "--repository=repo"}, &stdout, writer, "test")
			if ctx.Err() == nil || code != tc.code || strings.Contains(stderr.String(), "Interrupted:") {
				t.Fatalf("code = %d, context = %v, stdout = %q, stderr = %q", code, ctx.Err(), &stdout, &stderr)
			}
		})
	}
}

func TestArchiveCancellationDuringCleanup(t *testing.T) {
	path := archiveBorgEnvironment(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.WriteFile(path, []byte(`{"archives":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		exit string
		code int
	}{{"0", 0}, {"1", 1}, {"2", 2}, {"130", 130}} {
		t.Run(tc.exit, func(t *testing.T) {
			t.Setenv("LIST_BORG_READY", filepath.Join(t.TempDir(), "ready"))
			t.Setenv("LIST_BORG_EXIT", tc.exit)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stdout, stderr bytes.Buffer
			code := Run(ctx, []string{"archive", "list", "--repository=repo"}, &stdout, cancelOnText{&stderr, "cleanup signal", func() { cancel(cancellation.Signal(syscall.SIGTERM)) }}, "test")
			if ctx.Err() == nil || code != tc.code || strings.Contains(stderr.String(), "Interrupted:") {
				t.Fatalf("code = %d, context = %v, stdout = %q, stderr = %q", code, ctx.Err(), &stdout, &stderr)
			}
		})
	}
}

func TestInterruptedInvocation(t *testing.T) {
	for _, signal := range []cancellation.Signal{cancellation.Signal(syscall.SIGINT), cancellation.Signal(syscall.SIGTERM)} {
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(signal)
		var stdout, stderr bytes.Buffer
		if got := Run(ctx, nil, &stdout, &stderr, "test"); got != 128+int(signal) {
			t.Fatalf("exit code = %d", got)
		}
		if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Interrupted:") || strings.Contains(stderr.String(), "usage") {
			t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
		}
	}
}
