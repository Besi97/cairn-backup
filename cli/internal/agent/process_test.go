package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/runlock"
)

// Test-only process entry: the real agent and executor with a deterministic
// startup clock. No production flags or environment variables alter scheduling.
func scheduledAgentHelper(path string) int {
	options, err := config.Environment()
	if err != nil {
		return 3
	}
	options.Path, options.Explicit = path, true
	selected, err := config.Load(options)
	if err != nil {
		return 3
	}
	a, err := New(selected)
	if err != nil {
		return 3
	}
	a.clock = &jumpClock{testClock: testClock{now: instant("2026-01-01T00:00:00Z")}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx, os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
		return 2
	}
	return 0
}

type jumpClock struct {
	testClock
	jumped bool
}

func (c *jumpClock) NewTimer(d time.Duration) timer {
	if !c.jumped {
		c.jumped = true
		c.set(c.Now().Add(time.Minute))
		d = 0
	}
	return wallClock{}.NewTimer(d)
}

func TestScheduledAgentProcessLocks(t *testing.T) {
	base := t.TempDir()
	binary := filepath.Join(base, "borg")
	build := exec.Command("go", "build", "-o", binary, "../borg/testdata/fakeborg")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Borg: %v\n%s", err, output)
	}
	for _, finish := range []string{"shutdown", "crash"} {
		t.Run(finish, func(t *testing.T) {
			selected := loadTestConfig(t, definition("nightly", "* * * * *", "UTC"))
			ready := filepath.Join(t.TempDir(), "borg-tree")
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(t.TempDir(), "agent-output"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.Command(self)
			cmd.Env = append(os.Environ(), "CAIRN_SCHEDULED_AGENT_HELPER="+selected.Path,
				"PATH="+base+":"+os.Getenv("PATH"), "FAKE_VERSION=borg 1.4.5",
				"FAKE_TREE="+ready, "FAKE_BLOCK_PROBE=", "FAKE_ROLE=", "FAKE_IGNORE=")
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = cmd.Wait()
				close(done)
			}()
			t.Cleanup(func() {
				select {
				case <-done:
				default:
					cmd.Process.Kill()
					<-done
				}
			})
			parent := waitProcessFile(t, ready+".parent")
			waitProcessFile(t, ready+".child")
			pid, err := strconv.Atoi(strings.TrimSpace(parent))
			if err != nil {
				t.Fatal(err)
			}
			active := true
			t.Cleanup(func() {
				if active {
					syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
			identity, err := runlock.Config(selected.Path)
			if err != nil {
				t.Fatal(err)
			}
			store, err := runlock.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			acquisitions := []func() (*os.File, error){
				func() (*os.File, error) { return store.Agent(identity) },
				func() (*os.File, error) { return store.Workflow(identity, "nightly") },
			}
			assertBusy := func() {
				t.Helper()
				for _, acquire := range acquisitions {
					if lock, err := acquire(); !errors.Is(err, runlock.ErrBusy) {
						if lock != nil {
							lock.Close()
						}
						t.Fatalf("scheduled agent/workflow lock not held: %v", err)
					}
				}
			}
			assertBusy()
			if finish == "crash" {
				cmd.Process.Kill()
				<-done
				assertBusy()
				syscall.Kill(-pid, syscall.SIGKILL)
			} else {
				cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
					if waitErr != nil {
						log.Seek(0, io.SeekStart)
						data, _ := io.ReadAll(log)
						t.Fatalf("shutdown: %v\n%s", waitErr, data)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("scheduled agent did not finish shutdown")
				}
			}
			deadline := time.Now().Add(10 * time.Second)
			for _, acquire := range acquisitions {
				for {
					lock, err := acquire()
					if err == nil {
						lock.Close()
						break
					}
					if !errors.Is(err, runlock.ErrBusy) || time.Now().After(deadline) {
						t.Fatalf("lock not released: %v", err)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			active = false
		})
	}
}

func waitProcessFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
