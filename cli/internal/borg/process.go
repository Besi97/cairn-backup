package borg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

const shutdownGrace = 5 * time.Second

// runProcess owns a separate local process group, including children retaining
// output pipes after the leader exits. Only the direct child can be reaped here;
// orphan descendants are reaped by their ancestor or the system's init process.
func runProcess(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Detached descendants are outside group ownership; do not let inherited
	// pipes from such a process block output draining forever.
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- awaitLeader(cmd.Process.Pid) }()
	var waitErr error
	signal := syscall.SIGTERM
	observed := false
	select {
	case waitErr = <-exited:
		observed = true
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		signal = cancellation.From(ctx)
	}
	// Do not reap the group leader until group cleanup finishes. Its retained
	// PID reserves the group ID, so a delayed signal cannot hit a reused ID.
	cleanupErr := stopGroup(cmd.Process.Pid, signal, grace)
	if !observed {
		waitErr = <-exited
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), cleanupErr, waitErr)
	}
	if cleanupErr != nil || waitErr != nil {
		return &lifecycleError{errors.Join(err, cleanupErr, waitErr)}
	}
	return err
}

// A lifecycle failure must not be hidden by a joined Borg warning exit status.
type lifecycleError struct{ error }

func (e *lifecycleError) Unwrap() error { return e.error }

func awaitLeader(pid int) error {
	for {
		// Linux waitid permits a nil siginfo pointer. WNOWAIT observes exit
		// without reaping; Cmd.Wait remains the sole owner of reaping and IO.
		const pPID = 1
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, pPID, uintptr(pid), 0, syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return fmt.Errorf("observe Borg process exit: %w", errno)
		}
		return nil
	}
}

func stopGroup(pgid int, signal syscall.Signal, grace time.Duration) error {
	if err := syscall.Kill(-pgid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop Borg process group: %w", err)
	}
	deadline := time.Now().Add(grace)
	escalated := false
	for groupRunning(pgid) {
		if time.Now().After(deadline) {
			if escalated {
				return errors.New("Borg process group did not stop after SIGKILL")
			}
			if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill Borg process group: %w", err)
			}
			escalated = true
			deadline = time.Now().Add(grace)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// kill(-pgid, 0) also sees zombies, which Cairn cannot reap unless they are its
// direct children. Linux /proc lets us wait for execution to stop, not for init
// to reap orphan zombies. A live member keeps the process-group ID reserved.
func groupRunning(pgid int) bool {
	if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
		return false
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true // conservatively retain cleanup if procfs is unavailable
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue // exited while scanning
		}
		// comm can contain spaces and parentheses; fields begin after its last ).
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		parts := strings.Fields(string(data)[end+1:])
		if len(parts) < 3 {
			continue
		}
		group, _ := strconv.Atoi(parts[2])
		if group == pgid && parts[0] != "Z" && parts[0] != "X" {
			return true
		}
	}
	return false
}
