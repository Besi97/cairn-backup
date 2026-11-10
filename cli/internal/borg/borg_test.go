package borg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeVersion(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "borg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestDetectVersionPolicy(t *testing.T) {
	for _, test := range []struct {
		output  string
		warning bool
		err     string
	}{
		{output: "borg 1.4.5", warning: false},
		{output: "borg 1.4.9", warning: false},
		{output: "borg 1.4.4", warning: true},
		{output: "borg 1.4.5rc1", warning: true},
		{output: "borg 1.4.0b1", warning: true},
		{output: "borg 1.5.0", warning: true},
		{output: "borg 2.0.0b26.dev184", err: "not supported"},
		{output: "borg 0.9", err: "cannot identify"},
		{output: "not borg", err: "cannot identify"},
	} {
		t.Run(test.output, func(t *testing.T) {
			fakeVersion(t, test.output)
			installation, err := Detect(context.Background())
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("error = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (installation.Warning != "") != test.warning {
				t.Fatalf("warning = %q, want warning %v", installation.Warning, test.warning)
			}
		})
	}
}

func TestDetectMissingBorg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Detect(context.Background()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}
