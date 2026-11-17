package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const tuiConfig = `version: 1
repositories:
  local:
    location: /not-accessed
    passphrase: never-display-this
  remote:
    location: ssh://backup@example.invalid/./repo
    passphrase_file: /not-accessed
    identity_file: /not-accessed
workflows:
  nightly:
    cron: "* * * * *"
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_prefix: docs
            sources: [/not-accessed]
`

type terminalSession struct {
	file   *os.File
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	output strings.Builder
	update chan struct{}
}

func startTerminal(t *testing.T, binary, dir string, args ...string) *terminalSession {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + filepath.Join(dir, "bin"), "HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "TERM=xterm-256color"}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		t.Fatal(err)
	}
	s := &terminalSession{file: file, cmd: cmd, done: make(chan struct{}), update: make(chan struct{}, 1)}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := file.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.output.Write(buf[:n])
				s.mu.Unlock()
				select {
				case s.update <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_ = cmd.Wait()
		close(s.done)
	}()
	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = cmd.Process.Kill()
			<-s.done
		}
		_ = file.Close()
		<-readDone
		if strings.Contains(s.text(), "never-display-this") {
			t.Error("terminal output exposed configuration secret")
		}
	})
	return s
}

func (s *terminalSession) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

func (s *terminalSession) waitFor(t *testing.T, text string) {
	t.Helper()
	s.waitForAfter(t, text, 0)
}

func (s *terminalSession) waitForAfter(t *testing.T, text string, offset int) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		if strings.Contains(s.text()[offset:], text) {
			return
		}
		select {
		case <-s.update:
		case <-timeout.C:
			t.Fatalf("terminal did not show %q: %q", text, s.text())
		}
	}
}

func (s *terminalSession) exit(t *testing.T, code int) {
	t.Helper()
	select {
	case <-s.done:
		if got := s.cmd.ProcessState.ExitCode(); got != code {
			t.Fatalf("exit = %d, want %d; terminal: %q", got, code, s.text())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("terminal program did not exit")
	}
}

func writeTUIConfig(t *testing.T, dir, contents string) string {
	t.Helper()
	path := filepath.Join(dir, "cairn", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTUITerminal(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	t.Run("unavailable history leaves fullscreen browsing alone", func(t *testing.T) {
		for _, args := range [][]string{nil, {"tui"}} {
			dir := t.TempDir()
			writeTUIConfig(t, dir, tuiConfig)
			state := filepath.Join(dir, ".local", "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			blocker := filepath.Join(state, "cairn")
			if err := os.WriteFile(blocker, []byte("not-a-directory"), 0600); err != nil {
				t.Fatal(err)
			}
			s := startTerminal(t, binary, dir, args...)
			s.waitFor(t, "Configuration:")
			s.waitFor(t, "\x1b[?1049h")
			if _, err := io.WriteString(s.file, "q"); err != nil {
				t.Fatal(err)
			}
			s.exit(t, 0)
			s.waitFor(t, "\x1b[?1049l")
			if strings.Contains(s.text(), "Warning: local run history") ||
				strings.Contains(s.text(), "lifecycle event delivery failed") {
				t.Fatalf("history polluted fullscreen TUI: %q", s.text())
			}
			raw, err := os.ReadFile(blocker)
			if err != nil || string(raw) != "not-a-directory" {
				t.Fatalf("read-only browsing changed state: %q %v", raw, err)
			}
		}
	})
	t.Run("launch navigation and resize", func(t *testing.T) {
		for _, args := range [][]string{
			nil,
			{"tui"},
			{"--config", "cairn/config.yaml"},
			{"tui", "--config", "cairn/config.yaml"},
			{"--config", "cairn/config.yaml", "tui"},
		} {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				dir := t.TempDir()
				writeTUIConfig(t, dir, tuiConfig)
				s := startTerminal(t, binary, dir, args...)
				s.waitFor(t, "Configuration:")
				s.waitFor(t, "\x1b[?1049h")
				if _, err := io.WriteString(s.file, "?"); err != nil {
					t.Fatal(err)
				}
				s.waitFor(t, "Keyboard help")
				if err := pty.Setsize(s.file, &pty.Winsize{Rows: 4, Cols: 20}); err != nil {
					t.Fatal(err)
				}
				s.waitFor(t, "Resize terminal")
				if _, err := io.WriteString(s.file, "q"); err != nil {
					t.Fatal(err)
				}
				s.exit(t, 0)
				s.waitFor(t, "\x1b[?1049l")
			})
		}
	})
	t.Run("explicit selection overrides invalid default", func(t *testing.T) {
		dir := t.TempDir()
		writeTUIConfig(t, dir, "invalid YAML: [\n")
		path := filepath.Join(dir, "chosen.yaml")
		if err := os.WriteFile(path, []byte(tuiConfig), 0o600); err != nil {
			t.Fatal(err)
		}
		s := startTerminal(t, binary, dir, "--config", "chosen.yaml", "tui")
		s.waitFor(t, "Configuration:")
		if _, err := io.WriteString(s.file, "q"); err != nil {
			t.Fatal(err)
		}
		s.exit(t, 0)
	})
	t.Run("browse and reload selected snapshot", func(t *testing.T) {
		dir := t.TempDir()
		path := writeTUIConfig(t, dir, tuiConfig)
		s := startTerminal(t, binary, dir, "tui")
		s.waitFor(t, "Configuration:")
		send := func(keys, want string) {
			t.Helper()
			offset := len(s.text())
			if _, err := io.WriteString(s.file, keys); err != nil {
				t.Fatal(err)
			}
			s.waitForAfter(t, want, offset)
		}
		send("\t", "enter workflow")
		send("\r", "Cron: * * * * *")
		send("\r", "Referenced by nightly")
		send("y", "Section: repositories / local")
		send("\x1b", "y repository YAML")
		send("\x1b", "Stages run in order")
		send("\x1b", "enter workflow")
		send("\t", "Normalized YAML; configured paths")
		if err := os.WriteFile(path, []byte("version: never-display-this\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		send("r", "Previous TUI snapshot retained")
		writeTUIConfig(t, dir, strings.ReplaceAll(tuiConfig, "nightly", "reloaded-workflow"))
		send("r", "Agent snapshot unchanged")
		send("\x1b", "Read-only terminal interface.")
		send("\t", "reloaded-workflow")
		if _, err := io.WriteString(s.file, "q"); err != nil {
			t.Fatal(err)
		}
		s.exit(t, 0)
		s.waitFor(t, "\x1b[?1049l")
	})
	t.Run("configuration errors before terminal takeover", func(t *testing.T) {
		for _, tt := range []struct {
			args []string
			want string
		}{
			{[]string{"--config", ""}, "--config must not be empty"},
			{[]string{"tui", "--config", ""}, "--config must not be empty"},
			{[]string{"tui", "--config", "missing.yaml"}, "read configuration"},
			{nil, "version"},
		} {
			dir := t.TempDir()
			writeTUIConfig(t, dir, "version: never-display-this\nrepositories: {}\nworkflows: {}\n")
			s := startTerminal(t, binary, dir, tt.args...)
			s.exit(t, 3)
			s.waitFor(t, tt.want)
			if strings.Contains(s.text(), "\x1b[?1049h") {
				t.Fatal("invalid configuration took over terminal")
			}
		}
	})
	t.Run("help ignores invalid configuration", func(t *testing.T) {
		for _, args := range [][]string{
			{"--config", "missing.yaml", "--help"},
			{"tui", "--config", "missing.yaml", "--help"},
		} {
			s := startTerminal(t, binary, t.TempDir(), args...)
			s.exit(t, 0)
			s.waitFor(t, "Usage:")
			if strings.Contains(s.text(), "\x1b[?1049h") {
				t.Fatal("help started terminal interface")
			}
		}
	})
	t.Run("interrupt and termination", func(t *testing.T) {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			dir := t.TempDir()
			writeTUIConfig(t, dir, tuiConfig)
			s := startTerminal(t, binary, dir, "tui")
			s.waitFor(t, "Configuration:")
			if err := s.cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			s.exit(t, 128+int(sig))
			s.waitFor(t, "\x1b[?1049l")
		}
		dir := t.TempDir()
		writeTUIConfig(t, dir, tuiConfig)
		s := startTerminal(t, binary, dir, "tui")
		s.waitFor(t, "Configuration:")
		if _, err := s.file.Write([]byte{3}); err != nil {
			t.Fatal(err)
		}
		s.exit(t, 130)
		s.waitFor(t, "\x1b[?1049l")
	})
}

func TestTUIPartialTerminal(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	for _, terminals := range []string{"neither", "stdin only", "stdout only"} {
		t.Run(terminals, func(t *testing.T) {
			for _, explicit := range []bool{false, true} {
				master, slave, err := pty.Open()
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				defer slave.Close()
				args := []string{"--config", "/missing/config.yaml"}
				if explicit {
					args = append([]string{"tui"}, args...)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if terminals == "stdin only" {
					cmd.Stdin = slave
				}
				if terminals == "stdout only" {
					cmd.Stdout = slave
				}
				err = cmd.Run()
				if terminals == "stdout only" {
					_ = slave.Close()
					output, _ := io.ReadAll(master)
					stdout.Write(output)
				}
				if explicit {
					if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
						t.Fatalf("explicit tui error = %v", err)
					}
					if !strings.Contains(stderr.String(), "requires terminal standard input and output") {
						t.Fatalf("stderr = %q", &stderr)
					}
				} else {
					if err != nil || stderr.Len() > 0 {
						t.Fatalf("bare invocation: %v; stderr = %q", err, &stderr)
					}
					if !strings.Contains(stdout.String(), "Usage:") {
						t.Fatalf("stdout = %q", &stdout)
					}
				}
			}
		})
	}
}

func TestTUIArchiveTerminal(t *testing.T) {
	binary := buildExecutable(t, "cairn", ".")
	fake := buildExecutable(t, "borg", "../../internal/tui/testdata/browserborg")
	for _, quit := range []string{"q", "\x03", "signal"} {
		t.Run(quit, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(fake, filepath.Join(dir, "bin", "borg")); err != nil {
				t.Fatal(err)
			}
			repository := filepath.Join(dir, "repo")
			writeTUIConfig(t, dir, strings.ReplaceAll(tuiConfig, "/not-accessed", repository))
			s := startTerminal(t, binary, dir, "tui")
			s.waitFor(t, "Configuration:")
			send := func(keys, want string) {
				t.Helper()
				offset := len(s.text())
				if _, err := io.WriteString(s.file, keys); err != nil {
					t.Fatal(err)
				}
				s.waitForAfter(t, want, offset)
			}
			send("\t\t\t", "enter list archives")
			send("\r", "2026-11-16  files")
			send("\r", "TRUNCATED")
			send("/needle\r", "last/needle")
			send("\r", "Size: 4999 bytes")
			send("\x1b", "Search (literal")
			send("\x1b", "2026-11-16  slow")
			if _, err := io.WriteString(s.file, "j\r"); err != nil {
				t.Fatal(err)
			}
			// The fake emits a marker after installing its signal handler.
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(repository + ".running"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("slow listing did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := pty.Setsize(s.file, &pty.Winsize{Rows: 12, Cols: 60}); err != nil {
				t.Fatal(err)
			}
			send("?", "Keyboard help")
			code := 0
			if quit == "signal" {
				if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				code = 143
			} else {
				if _, err := io.WriteString(s.file, quit); err != nil {
					t.Fatal(err)
				}
				if quit == "\x03" {
					code = 130
				}
			}
			s.exit(t, code)
			s.waitFor(t, "\x1b[?1049l")
			if _, err := os.Stat(repository + ".stopped"); err != nil {
				t.Fatal("TUI exited before listing process cleanup:", err)
			}
			if strings.Contains(s.text(), "\x1b]0;unsafe") {
				t.Fatal("Borg diagnostics reached terminal")
			}
		})
	}
}
