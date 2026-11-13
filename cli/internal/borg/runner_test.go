package borg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var fakeBorg string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cairn-fake-borg-")
	if err != nil {
		panic(err)
	}
	fakeBorg = filepath.Join(dir, "borg")
	build := exec.Command("go", "build", "-o", fakeBorg, "./testdata/fakeborg")
	if out, err := build.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		panic(string(out) + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func request() Request {
	return Request{Args: []string{"create", "repo::archive", "source"}, Env: map[string]string{"FAKE_VERSION": "borg 1.4.5"}}
}

func TestSupportedVersion(t *testing.T) {
	for _, version := range []string{"borg 1.2.8", "borg 1.2.9", "borg 1.4.5", "borg 1.10.0", "borg 1.4.5\n"} {
		if err := supportedVersion(version); err != nil {
			t.Errorf("%q: %v", version, err)
		}
	}
	for _, version := range []string{"", "borg 1.1.18", "borg 1.2.7", "borg 2.0.0", "borg 2.0.0b1", "borg 1.5.0rc1", "borg 1.4.5.dev1", "borg 1.4", "unexpected", "borg 1.99999999999999999999999999999.0", "borg 1.4.5\nextra"} {
		if err := supportedVersion(version); err == nil {
			t.Errorf("accepted %q", version)
		}
	}
}

func TestInvocation(t *testing.T) {
	t.Setenv("FAKE_VALUE", "inherited")
	work := t.TempDir()
	record := filepath.Join(t.TempDir(), "invocation.json")
	req := request()
	req.Dir = work
	req.Args = []string{"create", "--exclude", "*.tmp", "repo with spaces::{hostname}", "source path", "; touch unwanted", "$(false)", "--", "-source"}
	req.Env["FAKE_RECORD"], req.Env["FAKE_VALUE"] = record, "override"
	var diagnostics bytes.Buffer
	req.Diagnostics = &diagnostics
	result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
	if err != nil || result.Outcome != Success || result.ExitCode != 0 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Args  []string
		Dir   string
		Value string
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Args, req.Args) || got.Dir != work || got.Value != "override" {
		t.Errorf("invocation = %+v", got)
	}
	if os.Getenv("FAKE_VALUE") != "inherited" {
		t.Error("parent environment changed")
	}
	for _, text := range []string{"stdout diagnostic", "stderr diagnostic"} {
		if !strings.Contains(diagnostics.String(), text) {
			t.Errorf("missing %q in %q", text, &diagnostics)
		}
	}
	if strings.Contains(diagnostics.String(), "borg 1.4.5") {
		t.Error("version probe leaked into operation diagnostics")
	}
}

func TestPathLookup(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(fakeBorg))
	result, err := (Runner{}).Run(t.Context(), request())
	if err != nil || result.Outcome != Success {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestRelativeExecutableWithWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, fakeBorg)
	if err != nil {
		t.Fatal(err)
	}
	req := request()
	req.Dir = t.TempDir()
	result, err := (Runner{Executable: relative}).Run(t.Context(), req)
	if err != nil || result.Outcome != Success {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestOperationLaunchFailure(t *testing.T) {
	data, err := os.ReadFile(fakeBorg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "borg")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	req := request()
	req.Env["FAKE_REMOVE_AFTER_PROBE"] = "yes"
	result, err := (Runner{Executable: path}).Run(t.Context(), req)
	if err == nil || result.Outcome != Failure || result.ExitCode != -1 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDiagnosticWriteFailure(t *testing.T) {
	want := errors.New("diagnostic destination failed")
	req := request()
	req.Diagnostics = failingWriter{want}
	result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
	if result.Outcome != Failure || !errors.Is(err, want) {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestEnvironmentInheritance(t *testing.T) {
	t.Setenv("FAKE_VALUE", "inherited")
	req := request()
	record := filepath.Join(t.TempDir(), "record")
	req.Env["FAKE_RECORD"] = record
	if _, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Value string }
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != "inherited" {
		t.Errorf("inherited value = %q", got.Value)
	}
}

func TestPreflightFailure(t *testing.T) {
	for _, tt := range []struct {
		name       string
		version    string
		probeExit  string
		executable string
	}{
		{name: "unsupported", version: "borg 1.2.7", executable: fakeBorg},
		{name: "prerelease", version: "borg 1.5.0rc1", executable: fakeBorg},
		{name: "unrecognized", version: "not Borg", executable: fakeBorg},
		{name: "probe failed", version: "borg 1.4.5", probeExit: "2", executable: fakeBorg},
		{name: "missing binary", executable: filepath.Join(t.TempDir(), "missing")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := request()
			record := filepath.Join(t.TempDir(), "record")
			req.Env["FAKE_RECORD"], req.Env["FAKE_VERSION"] = record, tt.version
			req.Env["FAKE_VERSION_EXIT"] = tt.probeExit
			result, err := (Runner{Executable: tt.executable}).Run(t.Context(), req)
			if err == nil || result.Outcome != Failure || result.ExitCode != -1 {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
				t.Error("operation ran after preflight failure")
			}
		})
	}
}

func TestOutcomes(t *testing.T) {
	for _, tt := range []struct {
		code    string
		outcome Outcome
		signal  int
	}{
		{"0", Success, 0}, {"1", Warning, 0}, {"2", Failure, 0},
		{"3", Failure, 0}, {"99", Failure, 0}, {"100", Warning, 0},
		{"127", Warning, 0}, {"130", Interrupted, 2}, {"143", Interrupted, 15},
		{"255", Interrupted, 127},
	} {
		t.Run(tt.code, func(t *testing.T) {
			req := request()
			req.Env["FAKE_EXIT"], req.Env["BORG_EXIT_CODES"] = tt.code, "modern"
			result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
			exitCode, _ := strconv.Atoi(tt.code)
			if result.Outcome != tt.outcome || result.Signal != tt.signal || result.ExitCode != exitCode {
				t.Errorf("result = %+v, want outcome %v, signal %d", result, tt.outcome, tt.signal)
			}
			wantError := tt.outcome == Failure || tt.outcome == Interrupted
			if (err != nil) != wantError {
				t.Errorf("error = %v, want error %v", err, wantError)
			}
		})
	}
}

func TestSignalTermination(t *testing.T) {
	req := request()
	req.Env["FAKE_SIGNAL"] = "yes"
	result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), req)
	if err == nil || result.Outcome != Interrupted || result.Signal != int(syscall.SIGKILL) || result.ExitCode != -1 {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

type notifyWriter struct {
	bytes.Buffer
	once sync.Once
	seen chan struct{}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.once.Do(func() { close(w.seen) })
	return n, err
}

func TestStreamingAndCancellation(t *testing.T) {
	for _, cancelOperation := range []bool{false, true} {
		t.Run(map[bool]string{false: "stream", true: "cancel"}[cancelOperation], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req := request()
			release := filepath.Join(t.TempDir(), "release")
			req.Env["FAKE_RELEASE"] = release
			writer := &notifyWriter{seen: make(chan struct{})}
			req.Diagnostics = writer
			type completed struct {
				result Result
				err    error
			}
			done := make(chan completed, 1)
			go func() {
				result, err := (Runner{Executable: fakeBorg}).Run(ctx, req)
				done <- completed{result, err}
			}()
			select {
			case <-writer.seen:
			case <-ctx.Done():
				t.Fatal("no diagnostic arrived before process exit")
			}
			select {
			case <-done:
				t.Fatal("operation completed before release")
			default:
			}
			if cancelOperation {
				cancel()
			} else if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-done:
				if cancelOperation {
					if got.result.Outcome != Interrupted || !errors.Is(got.err, context.Canceled) {
						t.Fatalf("result = %+v, error = %v", got.result, got.err)
					}
				} else if got.result.Outcome != Success || got.err != nil {
					t.Fatalf("result = %+v, error = %v", got.result, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not return")
			}
		})
	}
}

func TestCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := (Runner{Executable: fakeBorg}).Run(ctx, request())
	if result.Outcome != Interrupted || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}

func TestEmptyArguments(t *testing.T) {
	result, err := (Runner{Executable: fakeBorg}).Run(t.Context(), Request{})
	if result.Outcome != Failure || err == nil {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
}
