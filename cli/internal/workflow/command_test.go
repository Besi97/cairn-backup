package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

func TestConfiguredCommandWorkflow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	text := "version: 1\nrepositories: {}\nworkflows:\n  test:\n    stages:\n      - tasks:\n          - {type: command, program: /bin/echo, args: ['a b', '$(literal)'], timeout_seconds: 5}\n"
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
			}
		}
	}
	os.Exit(2)
}

func TestCommandExecution(t *testing.T) {
	t.Setenv("CAIRN_COMMAND_HELPER", "1")
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
