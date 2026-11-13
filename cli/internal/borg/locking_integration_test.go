package borg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type lockHolder struct {
	owner   int
	done    chan struct{}
	release string
	err     error // read only after done closes
}

func holdRepository(t *testing.T, ctx context.Context, binary, repository string) *lockHolder {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	dir := t.TempDir()
	holder := &lockHolder{done: make(chan struct{}), release: filepath.Join(dir, "release")}
	cmd := exec.Command(binary, "with-lock", "--lock-wait", "0", repository, fakeBorg)
	cmd.Env = environment(map[string]string{
		"BORG_PASSPHRASE": "", "FAKE_RECORD": filepath.Join(dir, "ready"), "FAKE_RELEASE": holder.release,
	})
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	go func() {
		holder.err = runProcess(ctx, cmd, shutdownGrace)
		close(holder.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-holder.done:
		case <-time.After(15 * time.Second):
			t.Error("external Borg lock holder did not stop")
		}
	})
	// with-lock launches the helper only after acquiring Borg's locks.
	var record struct{ ParentPID int }
	if err := json.Unmarshal(awaitFile(t, filepath.Join(dir, "ready")), &record); err != nil {
		t.Fatal(err)
	}
	if record.ParentPID <= 1 {
		t.Fatal("invalid Borg lock-owner PID")
	}
	holder.owner = record.ParentPID
	return holder
}

func (h *lockHolder) releaseAndWait(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(h.release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.done:
		if h.err != nil {
			t.Fatalf("external lock holder: %v", h.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("external lock holder did not release")
	}
}

func awaitLockAttempt(t *testing.T, repository string) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		// Borg 1.x retains its temporary acquisition directory while waiting
		// for the exclusive lock. Observe this instead of parsing diagnostics
		// or assuming the process has reached lock acquisition after a sleep.
		matches, err := filepath.Glob(filepath.Join(repository, "lock.exclusive.*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) > 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Borg did not attempt lock acquisition")
		case <-tick.C:
		}
	}
}

func TestRealBorgContention(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	runner := Runner{Executable: binary}
	for _, scenario := range []string{"zero wait", "timeout", "release while waiting", "cancel while waiting", "killed holder"} {
		t.Run(scenario, func(t *testing.T) {
			repository := disposableRepository(t, ctx, binary)
			holder := holdRepository(t, ctx, binary, repository)
			run := func(ctx context.Context, wait int, diagnostics *bytes.Buffer) (Result, error) {
				t.Helper()
				args, err := LockWaitArgs(&wait)
				if err != nil {
					t.Fatal(err)
				}
				args = append(append([]string{"list"}, args...), repository)
				return runner.Run(ctx, Request{Args: args, Diagnostics: diagnostics, Env: map[string]string{"BORG_LOCK_WAIT": "99"}})
			}
			var output bytes.Buffer
			switch scenario {
			case "zero wait", "timeout":
				wait := 0
				if scenario == "timeout" {
					wait = 1
				}
				result, err := run(ctx, wait, &output)
				if err == nil || result.Outcome != Failure || output.Len() == 0 {
					t.Fatalf("lock failure = %+v, %v\n%s", result, err, &output)
				}
				holder.releaseAndWait(t)
			case "release while waiting", "cancel while waiting":
				waitingCtx, stop := context.WithCancel(ctx)
				defer stop()
				type completed struct {
					result Result
					err    error
				}
				done := make(chan completed, 1)
				args, _ := LockWaitArgs(newInt(30))
				args = append(append([]string{"list"}, args...), repository)
				go func() {
					result, err := runner.Run(waitingCtx, Request{Args: args, Diagnostics: &output})
					done <- completed{result, err}
				}()
				awaitLockAttempt(t, repository)
				select {
				case got := <-done:
					t.Fatalf("operation completed before lock release/cancellation: %+v", got)
				default:
				}
				if scenario == "cancel while waiting" {
					stop()
				} else {
					holder.releaseAndWait(t)
				}
				select {
				case got := <-done:
					if scenario == "cancel while waiting" {
						if got.result.Outcome != Interrupted || !errors.Is(got.err, context.Canceled) {
							t.Fatalf("cancellation = %+v, %v\n%s", got.result, got.err, &output)
						}
						select {
						case <-holder.done:
							t.Fatal("canceling contender stopped external lock holder")
						default:
						}
						holder.releaseAndWait(t)
					} else if got.result.Outcome != Success || got.err != nil {
						t.Fatalf("waiting operation = %+v, %v\n%s", got.result, got.err, &output)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("contender did not complete")
				}
			case "killed holder":
				// Kill Borg itself, allowing a packaged binary's launcher to
				// reap it. Killing the launcher simultaneously can leave an
				// orphan zombie until init reaps it; Borg sees zombies as alive.
				if err := syscall.Kill(holder.owner, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				<-holder.done
			}
			// Borg, not Cairn, handles any stale lock state after interruption.
			wait := 0
			if scenario == "killed holder" {
				// Zero wait can time out after removing a stale lock, before
				// Borg's next native acquisition attempt.
				wait = 2
			}
			result, err := run(ctx, wait, &output)
			if err != nil || result.Outcome != Success {
				t.Fatalf("subsequent operation = %+v, %v\n%s", result, err, &output)
			}
		})
	}
}

func TestRealBorgRunningCancellation(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	repository := disposableRepository(t, ctx, binary)
	runner := Runner{Executable: binary}
	runningCtx, stop := context.WithCancel(ctx)
	defer stop()
	dir := t.TempDir()
	record := filepath.Join(dir, "running")
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		// with-lock provides a deterministic running Borg operation: its
		// command only starts once repository access and locking succeeded.
		result, err := runner.Run(runningCtx, Request{
			Args: []string{"with-lock", "--lock-wait", "0", repository, fakeBorg}, Diagnostics: &output,
			Env: map[string]string{"FAKE_RECORD": record, "FAKE_RELEASE": filepath.Join(dir, "release")},
		})
		if result.Outcome != Interrupted {
			err = errors.New("running Borg did not report interruption")
		}
		done <- err
	}()
	awaitFile(t, record)
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("running cancellation: %v\n%s", err, &output)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("running Borg did not stop")
	}
	result, err := runner.Run(ctx, Request{Args: []string{"list", "--lock-wait", "2", repository}, Diagnostics: &output})
	if err != nil || result.Outcome != Success {
		t.Fatalf("access after interruption = %+v, %v\n%s", result, err, &output)
	}
}
