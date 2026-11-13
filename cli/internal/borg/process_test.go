package borg

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func awaitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return data
		}
		select {
		case <-deadline.C:
			t.Fatalf("process did not create %s", path)
		case <-tick.C:
		}
	}
}

func TestProcessGroupCancellation(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(strconv.FormatBool(ignore)+"/"+signal.String(), func(t *testing.T) {
				base := filepath.Join(t.TempDir(), "ready")
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				cmd := exec.Command(fakeBorg)
				env := map[string]string{"FAKE_TREE": base}
				if ignore {
					env["FAKE_IGNORE"] = "yes"
				}
				cmd.Env = environment(env)
				var output bytes.Buffer
				cmd.Stdout, cmd.Stderr = &output, &output
				done := make(chan error, 1)
				go func() { done <- runProcess(ctx, cmd, 100*time.Millisecond) }()
				parent, _ := strconv.Atoi(string(awaitFile(t, base+".parent")))
				awaitFile(t, base+".child")
				// A separate process group must not be affected by cancellation.
				other := exec.Command("sleep", "30")
				if err := other.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
				cancel(cancellation.Signal(signal))
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("error = %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("cancellation hung")
				}
				if groupRunning(parent) {
					t.Fatal("owned processes still running")
				}
				if err := other.Process.Signal(syscall.Signal(0)); err != nil {
					t.Fatal("unrelated process was stopped")
				}
				if !ignore {
					for _, role := range []string{"parent", "child"} {
						if got := string(awaitFile(t, base+"."+role+".signal")); got != strconv.Itoa(int(signal)) {
							t.Fatalf("%s received signal %s", role, got)
						}
					}
				} else if !cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled() {
					t.Fatal("resistant leader was not killed")
				}
			})
		}
	}
}

func TestExitedLeaderWithOpenPipes(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(fakeBorg)
	cmd.Env = environment(map[string]string{"FAKE_TREE": base, "FAKE_PARENT_EXIT": "yes", "FAKE_IGNORE": "yes"})
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := runProcess(ctx, cmd, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("cleanup after leader exit: %v", err)
	}
	if groupRunning(cmd.Process.Pid) {
		t.Fatal("descendant survived leader exit")
	}
}

func TestProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	base := filepath.Join(t.TempDir(), "ready")
	req := request()
	req.Env["FAKE_TREE"], req.Env["FAKE_BLOCK_PROBE"] = base, "yes"
	done := make(chan error, 1)
	go func() {
		result, err := (Runner{Executable: fakeBorg}).Run(ctx, req)
		if result.Outcome != Interrupted {
			err = errors.New("probe did not report interruption")
		}
		done <- err
	}()
	parent, _ := strconv.Atoi(string(awaitFile(t, base+".parent")))
	awaitFile(t, base+".child")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || groupRunning(parent) {
			t.Fatalf("probe cleanup failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe cancellation hung")
	}
}

func TestProcessPreCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := exec.Command(fakeBorg)
	if err := runProcess(ctx, cmd, time.Millisecond); !errors.Is(err, context.Canceled) || cmd.Process != nil {
		t.Fatalf("pre-canceled process was launched: %v", err)
	}
}

type cancelWriter struct{ cancel context.CancelFunc }

func (w cancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestCancellationRacesWithCompletion(t *testing.T) {
	for range 20 {
		ctx, cancel := context.WithCancel(t.Context())
		req := request()
		req.Diagnostics = cancelWriter{cancel}
		result, err := (Runner{Executable: fakeBorg}).Run(ctx, req)
		cancel()
		if result.Outcome != Interrupted || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled operation reported %+v, %v", result, err)
		}
	}
}

func TestIndependentOperations(t *testing.T) {
	var groups []int
	var cancels []context.CancelFunc
	var completions []chan error
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		base := filepath.Join(t.TempDir(), "ready")
		req := request()
		req.Env["FAKE_TREE"] = base
		done := make(chan error, 1)
		go func() {
			_, err := (Runner{Executable: fakeBorg}).Run(ctx, req)
			done <- err
		}()
		pid, _ := strconv.Atoi(string(awaitFile(t, base+".parent")))
		awaitFile(t, base+".child")
		groups = append(groups, pid)
		cancels = append(cancels, cancel)
		completions = append(completions, done)
	}
	for i := range 2 {
		cancels[i]()
		select {
		case err := <-completions[i]:
			if !errors.Is(err, context.Canceled) || groupRunning(groups[i]) {
				t.Fatalf("operation %d cleanup failed: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent cancellation hung")
		}
		if i == 0 && !groupRunning(groups[1]) {
			t.Fatal("canceling one operation stopped another")
		}
	}
}
