package cli

import (
	"bytes"
	"context"
	"strings"
	"syscall"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

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
