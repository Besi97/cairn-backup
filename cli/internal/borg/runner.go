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
	// Progress opts human operation output into Borg's structured log channel.
	// A separated Stdout remains untouched.
	Progress func(Progress) error
	Access   AccessOptions
	// Stdout separates result data from stderr diagnostics when non-nil.
	// Nil preserves the combined diagnostic stream used by existing operations.
	Stdout io.Writer
}

// Runner uses Borg from PATH unless Executable selects an explicit binary.
// Lock waiting is delegated to Borg through prepared operation arguments.
type Runner struct {
	Executable string
	// InheritedFiles keeps caller-owned advisory locks alive in Borg after a
	// Cairn crash. The caller closes them only after every Run has returned.
	// They follow credential descriptors and never alter BORG_PASSPHRASE_FD.
	InheritedFiles []*os.File
}

// Run checks compatibility before executing an operation. Warnings are completed
// operations and return a nil error. Failures and interruptions return an error.
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
	access, err := prepareAccess(req.Access, env)
	if err != nil {
		return failed, err
	}
	defer access.close()
	probe := exec.Command(path, "--version")
	probe.Dir, probe.Env = req.Dir, env
	probe.ExtraFiles = r.InheritedFiles
	var version cappedOutput
	probe.Stdout, probe.Stderr = &version, &version
	if err := runProcess(ctx, probe, shutdownGrace); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			result := processResult(probe)
			result.Outcome = Interrupted
			return result, errors.Join(ctx.Err(), err)
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
	args := req.Args
	if access.rsh != "" {
		args = append([]string{"--rsh", access.rsh}, args...)
	}
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env, cmd.ExtraFiles = req.Dir, access.env, access.files
	cmd.ExtraFiles = append(append([]*os.File(nil), cmd.ExtraFiles...), r.InheritedFiles...)
	cmd.Stdout, cmd.Stderr = output, output
	var logs, plain *logOutput
	if req.Progress != nil {
		// Common Borg options are accepted after the operation name. Keep the
		// option before positional inputs and their "--" separator.
		args = append([]string(nil), args...)
		index := 1
		if access.rsh != "" {
			index += 2
		}
		args = append(args[:index], append([]string{"--log-json"}, args[index:]...)...)
		cmd.Args = append([]string{path}, args...)
		logs = &logOutput{writer: output, progress: req.Progress}
		plain = &logOutput{writer: output, raw: true}
		cmd.Stdout, cmd.Stderr = plain, logs
	}
	var stdout *lockedWriter
	if req.Stdout != nil {
		stdout = &lockedWriter{writer: req.Stdout}
		cmd.Stdout = stdout
	}
	err = runProcess(ctx, cmd, shutdownGrace)
	var logErr error
	if logs != nil {
		logErr = errors.Join(plain.flush(), logs.flush())
	}
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		result := processResult(cmd)
		result.Outcome = Interrupted
		return result, errors.Join(ctx.Err(), err)
	}
	if stdout != nil && stdout.Err() != nil {
		result := processResult(cmd)
		result.Outcome = Failure
		return result, fmt.Errorf("write Borg results: %w", errors.Join(stdout.Err(), err))
	}
	if logErr != nil {
		result := processResult(cmd)
		result.Outcome = Failure
		return result, fmt.Errorf("write Borg diagnostics: %w", errors.Join(logErr, err))
	}
	if outputErr := output.Err(); outputErr != nil {
		// Cmd.Wait prefers an ExitError over its output-copy error. A writer
		// failure can close the pipe and give the child SIGPIPE, so retain the
		// original diagnostic error independently of the child's exit status.
		result := processResult(cmd)
		result.Outcome = Failure
		if !errors.Is(err, outputErr) {
			err = errors.Join(outputErr, err)
		}
		return result, fmt.Errorf("write Borg diagnostics: %w", err)
	}
	if err != nil {
		var exit *exec.ExitError
		var lifecycle *lifecycleError
		if errors.As(err, &lifecycle) || !errors.As(err, &exit) {
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
	err    error
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

func (w *lockedWriter) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Version output is small; cap capture without buffering arbitrary child output.
type cappedOutput struct{ data []byte }

func (b *cappedOutput) Write(p []byte) (int, error) {
	const limit = 4096
	n := min(len(p), limit-len(b.data))
	b.data = append(b.data, p[:n]...)
	return len(p), nil
}
