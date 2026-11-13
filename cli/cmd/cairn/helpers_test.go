package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func buildExecutable(t *testing.T, name, pkg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-ldflags=-X main.version=0.1.0-test", "-o", path, pkg)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return path
}

func awaitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", path)
		case <-tick.C:
		}
	}
}
