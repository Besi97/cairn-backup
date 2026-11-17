package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

func TestOperationBoardKeepsStdoutAndSignalContracts(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	fake := buildExecutable(t, "borg", "../../internal/borg/testdata/fakeborg")
	for _, sig := range []syscall.Signal{0, syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			before, err := term.GetState(slave.Fd())
			if err != nil {
				t.Fatal(err)
			}
			base := t.TempDir()
			ready, release := filepath.Join(base, "ready"), filepath.Join(base, "release")
			cmd := exec.Command(binary, "compact", "--repository", "repo")
			cmd.Dir = base
			cmd.Env = []string{"PATH=" + filepath.Dir(fake), "HOME=" + base, "TERM=xterm-256color", "FAKE_VERSION=borg 1.4.5"}
			if sig == 0 {
				cmd.Env = append(cmd.Env, "FAKE_RECORD="+ready, "FAKE_RELEASE="+release,
					"FAKE_LOG_RECORDS={\"type\":\"progress_percent\",\"message\":\"checking 50%\"}\n"+
						"{\"type\":\"log_message\",\"message\":\"permanent warning\"}\n")
			} else {
				cmd.Env = append(cmd.Env, "FAKE_TREE="+ready)
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, slave
			readDone := make(chan struct{})
			go func() { _, _ = io.Copy(&stderr, master); close(readDone) }()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				<-done
				slave.Close()
				master.Close()
				<-readDone
			})
			if sig == 0 {
				awaitFile(t, ready)
				if err := os.WriteFile(release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				awaitFile(t, ready+".parent")
				awaitFile(t, ready+".child")
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("operation did not stop")
			}
			after, err := term.GetState(slave.Fd())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("terminal modes changed: %v", err)
			}
			slave.Close()
			<-readDone
			text := stderr.String()
			wantCode, wantResult := 0, "Compact completed.\n"
			if sig != 0 {
				wantCode, wantResult = 128+int(sig), "Compact interrupted.\n"
				if strings.LastIndex(text, "\x1b[J") > strings.LastIndex(text, "Interrupted:") {
					t.Fatalf("board cleanup erased interruption: %q", text)
				}
			} else if !strings.Contains(text, "| borg: checking 50%") ||
				!strings.Contains(text, "borg [compact] permanent warning") || strings.Contains(text, `"type"`) {
				t.Fatalf("progress or diagnostic policy lost: %q", text)
			}
			if cmd.ProcessState.ExitCode() != wantCode || stdout.String() != wantResult {
				t.Fatalf("exit=%d stdout=%q stderr=%q", cmd.ProcessState.ExitCode(), stdout.String(), text)
			}
			if !strings.Contains(text, "cairn [compact] running") || !strings.Contains(text, "\x1b[J") ||
				strings.Contains(text, "\x1b[?1049") {
				t.Fatalf("missing inline board or fullscreen effect: %q", text)
			}
		})
	}
}
