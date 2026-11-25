package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

func TestConfiguredCommandWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	text := "version: 1\nrepositories: {}\nworkflows:\n  test:\n    stages:\n      - tasks:\n          - {type: command, program: echo, args: ['a b', '$(literal)'], timeout_seconds: 5}\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(cfg, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result, err := (Executor{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), plan)
	if err != nil || result.Outcome != borg.Success || !strings.Contains(stdout.String(), "a b $(literal)") {
		t.Fatalf("result=%+v err=%v stdout=%s stderr=%s", result, err, &stdout, &stderr)
	}
}

func TestCommandHelper(t *testing.T) {
	if os.Getenv("CAIRN_COMMAND_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			switch os.Args[i+1] {
			case "echo":
				fmt.Fprintf(os.Stdout, "%q", os.Args[i+2:])
				fmt.Fprint(os.Stderr, "diagnostic")
				os.Exit(0)
			case "fail":
				os.Exit(1)
			case "wait":
				time.Sleep(time.Minute)
			case "partial":
				fmt.Fprint(os.Stdout, "partial")
				time.Sleep(time.Minute)
			case "descendant":
				mode := "child"
				if len(os.Args) > i+3 {
					mode = os.Args[i+3]
				}
				child := exec.Command(os.Args[0], "-test.run=^TestCommandHelper$", "--", mode, os.Args[i+2])
				if child.Start() != nil {
					os.Exit(3)
				}
				for {
					if _, err := os.Stat(os.Args[i+2]); err == nil {
						os.Exit(0)
					}
					time.Sleep(time.Millisecond)
				}
			case "child":
				signal.Ignore(syscall.SIGTERM)
				if os.WriteFile(os.Args[i+2], []byte("ready"), 0600) != nil {
					os.Exit(4)
				}
				time.Sleep(1200 * time.Millisecond)
				os.Exit(0)
			case "child-term":
				signals := make(chan os.Signal, 1)
				signal.Notify(signals, syscall.SIGTERM)
				if os.WriteFile(os.Args[i+2], []byte("ready"), 0600) != nil {
					os.Exit(4)
				}
				<-signals
				_ = os.WriteFile(os.Args[i+2]+".stopped", []byte("stopped"), 0600)
				os.Exit(0)
			}
		}
	}
	os.Exit(2)
}

func TestCommandExecution(t *testing.T) {
	t.Setenv("CAIRN_COMMAND_HELPER", "1")
	// Race-instrumented helper processes otherwise pause for a second on exit.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) Task {
		return Task{Type: "command", Scope: "command", Command: &Command{
			Program: binary, Args: append([]string{"-test.run=^TestCommandHelper$", "--"}, args...),
			Dir: t.TempDir(), Timeout: time.Second * 10,
		}}
	}
	t.Run("literal arguments and output", func(t *testing.T) {
		var out, stderr bytes.Buffer
		plan := Plan{Name: "test", Stages: [][]Task{{command("echo", "a b", "$(not a shell)", "")}}}
		result, err := (Executor{Stdout: &out, Stderr: &stderr}).Run(context.Background(), plan)
		if err != nil || result.Outcome != borg.Success || !strings.Contains(out.String(), `"a b" "$(not a shell)" ""`) || !strings.Contains(stderr.String(), "diagnostic") {
			t.Fatalf("result=%+v err=%v stdout=%s stderr=%s", result, err, &out, &stderr)
		}
	})
	t.Run("nonzero stops later stage", func(t *testing.T) {
		plan := Plan{Name: "test", Stages: [][]Task{{command("fail")}, {command("echo")}}}
		result, err := (Executor{}).Run(context.Background(), plan)
		if err == nil || result.Outcome != borg.Failure || len(result.Stages) != 1 || result.Stages[0][0].Result.ExitCode != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		task := command("wait")
		task.Command.Timeout = 100 * time.Millisecond
		result, err := (Executor{}).Run(context.Background(), Plan{Stages: [][]Task{{task}}})
		if err == nil || !strings.Contains(err.Error(), "timed out") || result.Outcome != borg.Failure {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("leader exits before timeout while descendant cleans up", func(t *testing.T) {
		task := command("descendant", filepath.Join(t.TempDir(), "ready"))
		task.Command.Timeout = 600 * time.Millisecond
		start := time.Now()
		result, err := (Executor{}).Run(context.Background(), Plan{Stages: [][]Task{{task}}})
		if err != nil || result.Outcome != borg.Success || time.Since(start) < task.Command.Timeout {
			t.Fatalf("result=%+v err=%v elapsed=%v", result, err, time.Since(start))
		}
	})
	t.Run("leader exit stops descendant", func(t *testing.T) {
		ready := filepath.Join(t.TempDir(), "ready")
		task := command("descendant", ready, "child-term")
		result, err := (Executor{}).Run(context.Background(), Plan{Stages: [][]Task{{task}}})
		if err != nil || result.Outcome != borg.Success {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if _, err := os.Stat(ready + ".stopped"); err != nil {
			t.Fatalf("descendant did not receive termination: %v", err)
		}
	})
	t.Run("partial output streams before exit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		writes := make(chan string, 10)
		done := make(chan error, 1)
		go func() {
			_, err := (Executor{Stdout: commandWriter(writes)}).Run(ctx, Plan{Stages: [][]Task{{command("partial")}}})
			done <- err
		}()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case text := <-writes:
				if strings.Contains(text, "partial") {
					cancel()
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
					return
				}
			case <-timer.C:
				cancel()
				<-done
				t.Fatal("partial output was not streamed")
			}
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		timer := time.AfterFunc(100*time.Millisecond, cancel)
		defer timer.Stop()
		result, err := (Executor{}).Run(ctx, Plan{Stages: [][]Task{{command("wait")}}})
		if !errors.Is(err, context.Canceled) || result.Outcome != borg.Interrupted {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("startup failure", func(t *testing.T) {
		task := command("echo")
		task.Command.Program = "/nonexistent/cairn-command"
		result, err := (Executor{}).Run(context.Background(), Plan{Stages: [][]Task{{task}}})
		if err == nil || result.Outcome != borg.Failure || strings.Contains(err.Error(), task.Command.Program) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

type commandWriter chan string

func (w commandWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}
