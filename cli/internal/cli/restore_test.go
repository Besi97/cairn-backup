package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
	"github.com/creack/pty"
)

func fakeRestoreData(t *testing.T) string {
	t.Helper()
	fakeBorgEnvironment(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "list"), []byte("-\x00-rw-------\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "export-tar"), b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_RESTORE_DATA", dir)
	return dir
}

func restoreArgs(destination string) []string {
	return []string{"restore", "--repository=repo", "--archive-name=snapshot", "--destination=" + destination}
}

func TestRestoreInvalidInvocation(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("BORG_REPO", "/must-not-be-used")
	destination := filepath.Join(t.TempDir(), "new")
	argsList := [][]string{{"restore"}, {"restore", "--repository=repo"}, {"restore", "--repository=repo", "--archive-name=name"}}
	for _, extra := range []string{
		"--destination=", "--repository=", "--archive-name=", "--archive-name={now}", "--passphrase-file=",
		"--identity-file=", "--lock-wait=-1", "--lock-wait=1.5", "--lock-wait=", "--lock-wait=999999999999999999999",
		"--overwrite", "--yes", "--strip-components=1", "--exclude=*", "--config=missing", "--json",
		"--repository=repo::name", "--destination=/", "../outside", "/absolute", "a/../b", "a//b", ".",
	} {
		argsList = append(argsList, append(restoreArgs(destination), extra))
	}
	for _, args := range argsList {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 3 ||
			stdout.Len() != 0 || strings.Contains(stderr.String(), "find Borg") {
			t.Fatalf("args=%q code=%d stdout=%s stderr=%s", args, code, &stdout, &stderr)
		}
	}
}

func TestRestoreHelpAndCompletion(t *testing.T) {
	t.Setenv("PATH", "")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"restore", "--help"}, &stdout, &stderr, "test"); code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	for _, text := range []string{"--allow-existing", "may be replaced", "not a sandbox", "not modified concurrently", "--dry-run", "literal archive-relative"} {
		if !strings.Contains(stdout.String(), text) {
			t.Errorf("help missing %q", text)
		}
	}
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{""}, "restore"},
		{[]string{"restore", "--"}, "--allow-existing"},
		{[]string{"restore", "--destination", ""}, ":16\n"},
		{[]string{"restore", "--archive-name", ""}, ":4\n"},
		{[]string{"restore", ""}, ":4\n"},
		{[]string{"restore", "--repository", "ssh://user@host/repo"}, ":4\n"},
	} {
		code, out, errOut := runCompletion(t, tt.args...)
		if code != 0 || !strings.Contains(out, tt.want) {
			t.Fatalf("completion %q: code=%d stdout=%q stderr=%q", tt.args, code, out, errOut)
		}
	}
}

func TestRestoreInvocation(t *testing.T) {
	fakeRestoreData(t)
	t.Chdir(t.TempDir())
	cwd := mustGetwd(t)
	if err := os.WriteFile("cairn.yaml", []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	record, calls := filepath.Join(t.TempDir(), "record"), filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_CALLS", calls)
	t.Setenv("BORG_REPO", "/must-not-be-used")
	for _, preview := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		args := restoreArgs("new")
		args = append(args, "--lock-wait=08")
		if preview {
			args = append(args, "--dry-run")
		}
		args = append(args, "--", "-literal")
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
		got := readRecord(t, record)
		want := []string{"extract", "--lock-wait", "8", "--list", "--", filepath.Join(cwd, "repo") + "::snapshot", "pp:-literal"}
		wantDir := filepath.Join(cwd, "new")
		if preview {
			want = append([]string{"extract", "--dry-run"}, want[1:]...)
			wantDir = cwd
			if _, err := os.Stat("new"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preview created destination")
			}
		}
		if !reflect.DeepEqual(got.Args, want) || got.Dir != wantDir {
			t.Fatalf("invocation=%+v want args=%q dir=%q", got, want, wantDir)
		}
		if !strings.Contains(stdout.String(), "Restore completed") || !strings.Contains(stderr.String(), "not a sandbox") {
			t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil || bytes.Count(data, []byte("\n")) != 6 {
		t.Fatalf("expected three Borg commands per restore: %s %v", data, err)
	}
}

func TestRestoreConfirmation(t *testing.T) {
	fakeRestoreData(t)
	destination := t.TempDir()
	t.Setenv("PATH", "") // Lack of authorization must be checked before Borg.
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), restoreArgs(destination), &stdout, &stderr, "test"); code != 3 ||
		!strings.Contains(stderr.String(), "--allow-existing") || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	cmd := newRootCommand("test")
	cmd.SetArgs(restoreArgs(destination))
	cmd.SetIn(restoreUnreadableInput{})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Fatal("accepted nonterminal input")
	}
}

type restoreUnreadableInput struct{}

func (restoreUnreadableInput) Read([]byte) (int, error) { panic("read nonterminal input") }

func TestRestoreTerminalConfirmation(t *testing.T) {
	fakeRestoreData(t)
	for _, answer := range []string{"yes", "y", "no", ""} {
		t.Run(strconv.Quote(answer), func(t *testing.T) {
			master, terminal, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer terminal.Close()
			if _, err := fmt.Fprintln(master, answer); err != nil {
				t.Fatal(err)
			}
			cmd := newRootCommand("test")
			cmd.SetArgs(restoreArgs(t.TempDir()))
			cmd.SetIn(terminal)
			cmd.SetOut(terminal)
			var stderr bytes.Buffer
			cmd.SetErr(&stderr)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			err = cmd.ExecuteContext(ctx)
			if (err == nil) != (answer == "yes") || !strings.Contains(stderr.String(), "Conflicting existing files may be replaced") {
				t.Fatalf("answer=%q error=%v stderr=%s", answer, err, &stderr)
			}
		})
	}
}

func TestRestoreConfirmationCancellation(t *testing.T) {
	fakeRestoreData(t)
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer terminal.Close()
	cmd := newRootCommand("test")
	cmd.SetArgs(restoreArgs(t.TempDir()))
	cmd.SetIn(terminal)
	cmd.SetOut(terminal)
	cmd.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("confirmation cancellation=%v", err)
	}
}

func TestRestoreOutcomesAndEvents(t *testing.T) {
	fakeRestoreData(t)
	for _, preview := range []bool{false, true} {
		for _, raw := range []int{0, 1, 2, 130, 143} {
			t.Run(fmt.Sprintf("%v/%d", preview, raw), func(t *testing.T) {
				t.Setenv("FAKE_EXIT", strconv.Itoa(raw))
				args := append(restoreArgs(t.TempDir()), "--allow-existing")
				if preview {
					args = append(args, "--dry-run")
				}
				var events []lifecycle.Event
				ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
					events = append(events, event)
					return errors.New("secret-observer-error")
				})
				var stdout, stderr bytes.Buffer
				if code := Run(ctx, args, &stdout, &stderr, "test"); code != raw {
					t.Fatalf("code=%d stderr=%s", code, &stderr)
				}
				want := lifecycle.Success
				switch raw {
				case 1:
					want = lifecycle.Warning
				case 2:
					want = lifecycle.Failure
				case 130, 143:
					want = lifecycle.Interrupted
				}
				if len(events) != 4 || events[3].Outcome != want || events[2].Task != 1 {
					t.Fatalf("events=%+v", events)
				}
				for _, e := range events {
					if e.Operation != "restore" || e.Preview != preview || e.Source != lifecycle.Manual || e.Repository != "" {
						t.Fatalf("event=%+v", e)
					}
				}
				data, _ := json.Marshal(events)
				if strings.Contains(string(data), "snapshot") || strings.Contains(string(data), "destination") ||
					strings.Contains(stderr.String(), "secret-observer-error") {
					t.Fatal("sensitive event fields")
				}
			})
		}
	}
}

func TestRestorePreflightStopsBeforeWrites(t *testing.T) {
	for _, data := range []string{
		"../outside\x00-\x00-rw-------\x00",
		"link\x00l\x00lrwxrwxrwx\x00link/file\x00-\x00-rw-------\x00",
		"file\x00-\x00-rw-------", // truncated output
	} {
		dir := fakeRestoreData(t)
		if err := os.WriteFile(filepath.Join(dir, "list"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(t.TempDir(), "new")
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), restoreArgs(destination), &stdout, &stderr, "test"); code != 2 {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
		if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unsafe preflight created destination")
		}
	}
}

func TestRestorePreflightWarning(t *testing.T) {
	fakeRestoreData(t)
	t.Setenv("FAKE_PREFLIGHT_EXIT", "1")
	destination := filepath.Join(t.TempDir(), "new")
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), restoreArgs(destination), &stdout, &stderr, "test"); code != 1 ||
		!strings.Contains(stderr.String(), "extraction was not attempted") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preflight warning created destination")
	}
}

func TestRestoreCancelledAndOutputFailures(t *testing.T) {
	fakeRestoreData(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cancellation.Signal(syscall.SIGTERM))
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, restoreArgs(filepath.Join(t.TempDir(), "new")), &stdout, &stderr, "test"); code != 143 {
		t.Fatalf("cancel code=%d stderr=%s", code, &stderr)
	}
	destination := filepath.Join(t.TempDir(), "new")
	if code := Run(t.Context(), restoreArgs(destination), &stdout, brokenWriter{}, "test"); code != 2 {
		t.Fatalf("diagnostic failure code=%d", code)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("status output failure created destination")
	}
	if code := Run(t.Context(), restoreArgs(destination), brokenWriter{}, &stderr, "test"); code != 2 {
		t.Fatalf("result output failure code=%d stderr=%s", code, &stderr)
	}
}
