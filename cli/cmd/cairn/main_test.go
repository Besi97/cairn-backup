package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cli"
)

func TestBinary(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	for _, tt := range []struct {
		name string
		args []string
		code int
		out  string
	}{
		{name: "help without dependencies", args: []string{"--help"}, out: "Usage:"},
		{name: "backup help without dependencies", args: []string{"backup", "--help"}, out: "--repository"},
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

func TestSignalContextHelper(t *testing.T) {
	if os.Getenv("CAIRN_SIGNAL_HELPER") != "yes" {
		return
	}
	ctx, stop := signalContext()
	_, _ = os.Stdout.WriteString("ready\n")
	<-ctx.Done()
	code := cli.Run(ctx, nil, os.Stdout, os.Stderr, "test")
	stop()
	os.Exit(code)
}

func TestSignalExitCodes(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSignalContextHelper$")
			cmd.Env = append(os.Environ(), "CAIRN_SIGNAL_HELPER=yes")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-done
			})
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				ready <- scanner.Scan() && scanner.Text() == "ready"
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("signal helper did not become ready")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("signal registration timed out")
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				if cmd.ProcessState.ExitCode() != 128+int(sig) || !strings.Contains(stderr.String(), "Interrupted:") {
					t.Fatalf("exit = %v, stderr = %q", cmd.ProcessState, &stderr)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("signal helper did not exit")
			}
		})
	}
}
