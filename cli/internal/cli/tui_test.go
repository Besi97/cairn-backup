package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestTUINoninteractive(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 0, "Usage:"},
		{[]string{"--config", "/missing/config.yaml"}, 0, "Usage:"},
		{[]string{"--config", ""}, 0, "Usage:"},
		{[]string{"--config", "/missing/config.yaml", "--help"}, 0, "Usage:"},
		{[]string{"tui", "--help", "--config", "/missing/config.yaml"}, 0, "Usage:"},
		{[]string{"tui"}, 3, "requires terminal standard input and output"},
		{[]string{"--config", "/missing/config.yaml", "tui"}, 3, "requires terminal standard input and output"},
		{[]string{"tui", "--config", ""}, 3, "requires terminal standard input and output"},
		{[]string{"tui", "extra"}, 3, "unknown command"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(t.Context(), tt.args, &stdout, &stderr, "test")
			if code != tt.code || !strings.Contains(stdout.String()+stderr.String(), tt.want) {
				t.Fatalf("code = %d; stdout = %q; stderr = %q", code, &stdout, &stderr)
			}
			if strings.Contains(stdout.String()+stderr.String(), "\x1b") {
				t.Fatal("noninteractive command emitted terminal controls")
			}
		})
	}
}
