package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "cairn")
	build := exec.Command("go", "build", "-ldflags=-X main.version=0.1.0-test", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, tt := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{name: "help without dependencies", args: []string{"--help"}, out: "Usage:"},
		{name: "version metadata", args: []string{"--version"}, out: "cairn 0.1.0-test\n"},
		{name: "invalid invocation", args: []string{"--unknown"}, code: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binary, tt.args...)
			cmd.Dir = t.TempDir()
			// No Borg, repository, credentials, or existing user configuration.
			cmd.Env = []string{"PATH=", "HOME=" + cmd.Dir, "XDG_CONFIG_HOME=" + cmd.Dir}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tt.code {
				t.Fatalf("exit code = %d, want %d; stderr: %s", code, tt.code, &stderr)
			}
			if tt.code == 0 {
				if !strings.Contains(stdout.String(), tt.out) || stderr.Len() != 0 {
					t.Errorf("stdout = %q, stderr = %q", &stdout, &stderr)
				}
			} else if stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown flag") {
				t.Errorf("stdout = %q, stderr = %q", &stdout, &stderr)
			}
		})
	}
}

func TestDevelopmentVersion(t *testing.T) {
	if version != "dev" {
		t.Errorf("default version = %q, want dev", version)
	}
}
