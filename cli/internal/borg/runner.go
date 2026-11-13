// Package borg executes Borg independently of command parsing and configuration.
package borg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// Outcome describes an operation independently of Borg's exit-code mode.
type Outcome int

const (
	Failure Outcome = iota
	Success
	Warning
	Interrupted
)

// Result retains the raw process status. ExitCode is -1 when no numeric exit
// code is available; Signal identifies termination by a signal when known.
type Result struct {
	Outcome  Outcome
	ExitCode int
	Signal   int
}

// Request supplies already prepared Borg arguments, not shell syntax.
// Env overrides inherited variables without modifying the parent environment.
type Request struct {
	Args        []string
	Dir         string
	Env         map[string]string
	Diagnostics io.Writer
}

// Runner uses Borg from PATH unless Executable selects an explicit binary.
// It does not implement credential policy, lock policy, or process-tree cleanup.
type Runner struct {
	Executable string
}

// Run checks compatibility before executing an operation. Warnings are completed
// operations and return a nil error. Failures and interruptions return an error.
// Context cancellation currently stops only the direct child, not descendants.
func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	failed := Result{Outcome: Failure, ExitCode: -1}
	if err := ctx.Err(); err != nil {
		return Result{Outcome: Interrupted, ExitCode: -1}, err
	}
	if len(req.Args) == 0 {
		return failed, errors.New("missing Borg operation arguments")
	}
	name := r.Executable
	if name == "" {
		name = "borg"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return failed, fmt.Errorf("find Borg executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return failed, fmt.Errorf("resolve Borg executable: %w", err)
	}
	env := environment(req.Env)
	probe := exec.CommandContext(ctx, path, "--version")
	probe.Dir, probe.Env = req.Dir, env
	var version cappedOutput
	probe.Stdout, probe.Stderr = &version, &version
	if err := probe.Run(); err != nil {
		if ctx.Err() != nil {
			return Result{Outcome: Interrupted, ExitCode: -1}, ctx.Err()
		}
		return failed, fmt.Errorf("check Borg version: %w", err)
	}
	if err := supportedVersion(string(version.data)); err != nil {
		return failed, err
	}

	writer := req.Diagnostics
	if writer == nil {
		writer = io.Discard
	}
	output := &lockedWriter{writer: writer}
	cmd := exec.CommandContext(ctx, path, req.Args...)
	cmd.Dir, cmd.Env = req.Dir, env
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	if ctx.Err() != nil && err != nil {
		result := processResult(cmd)
		result.Outcome = Interrupted
		return result, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			result := processResult(cmd)
			result.Outcome = Failure
			return result, fmt.Errorf("run Borg: %w", err)
		}
	}
	result := processResult(cmd)
	switch result.Outcome {
	case Success, Warning:
		return result, nil
	case Interrupted:
		return result, fmt.Errorf("Borg interrupted by signal %d", result.Signal)
	default:
		return result, fmt.Errorf("Borg exited with code %d", result.ExitCode)
	}
}

func processResult(cmd *exec.Cmd) Result {
	result := Result{Outcome: Failure, ExitCode: -1}
	if cmd.ProcessState == nil {
		return result
	}
	result.ExitCode = cmd.ProcessState.ExitCode()
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		result.Outcome, result.Signal = Interrupted, int(status.Signal())
		return result
	}
	switch code := result.ExitCode; {
	case code == 0:
		result.Outcome = Success
	case code == 1 || code >= 100 && code <= 127:
		result.Outcome = Warning
	case code >= 128 && code <= 255:
		// Borg can also report a handled signal through 128 + signal.
		result.Outcome, result.Signal = Interrupted, code-128
	}
	return result
}

func environment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

// Version output is small; cap capture without buffering arbitrary child output.
type cappedOutput struct{ data []byte }

func (b *cappedOutput) Write(p []byte) (int, error) {
	const limit = 4096
	n := min(len(p), limit-len(b.data))
	b.data = append(b.data, p[:n]...)
	return len(p), nil
}
