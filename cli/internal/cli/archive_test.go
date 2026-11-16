package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

const listArchiveJSON = `{"name":"before\n\u001b[31mupgrade","id":"abc","start":"2026-11-15T10:00:00.000000"}`
const listFileJSON = `{"path":"dir/file\n\u001b[31mname","type":"l","mode":"lrwxrwxrwx","size":0,"mtime":"2026-11-15T10:00:00.000000","uid":1000,"gid":100,"linktarget":"target\nname","healthy":true}`

func archiveBorgEnvironment(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "borg")
	if out, err := exec.Command("go", "build", "-o", binary, "../archive/testdata/listborg").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	t.Setenv("PATH", filepath.Dir(binary))
	t.Setenv("LIST_BORG_WAIT", "")
	t.Setenv("LIST_BORG_EXIT", "")
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("LIST_BORG_DATA", data)
	return data
}

func TestArchiveOutput(t *testing.T) {
	path := archiveBorgEnvironment(t)
	for _, inspect := range []bool{false, true} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "inspect", false: "list"}[inspect], map[bool]string{true: "json", false: "human"}[jsonOutput]}, " "), func(t *testing.T) {
				data := `{"archives":[` + listArchiveJSON + `]}`
				args := []string{"archive", "list", "--repository=repo"}
				if inspect {
					data = listFileJSON + "\n"
					args[1] = "inspect"
					args = append(args, "--archive-name=before upgrade")
				}
				if jsonOutput {
					args = append(args, "--json")
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("BORG_REPO", "not-used")
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				var stdout, stderr bytes.Buffer
				if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 {
					t.Fatalf("code = %d, stdout = %s, stderr = %s", code, &stdout, &stderr)
				}
				if stderr.String() != "listing diagnostic\n" || strings.Contains(stdout.String(), "diagnostic") {
					t.Fatalf("stdout = %q, stderr = %q", &stdout, &stderr)
				}
				if strings.Count(stdout.String(), "\n") != 1 || strings.ContainsRune(stdout.String(), '\x1b') {
					t.Fatalf("unsafe/multiline output = %q", &stdout)
				}
				if jsonOutput {
					var entry map[string]any
					if err := json.Unmarshal(stdout.Bytes(), &entry); err != nil {
						t.Fatal(err)
					}
					key := "name"
					if inspect {
						key = "path"
					}
					if !strings.Contains(entry[key].(string), "\n\x1b") {
						t.Fatalf("JSON lost text: %v", entry)
					}
				} else if !strings.Contains(stdout.String(), `\n\x1b[31m`) {
					t.Fatalf("unquoted human text = %q", &stdout)
				}
			})
		}
	}
}

func TestArchiveInvalidInputs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"list"}, {"list", "--repository="}, {"inspect", "--repository=repo"},
		{"inspect", "--repository=repo", "--archive-name="},
		{"list", "--repository=repo::archive"}, {"list", "--repository=repo", "--archive-name=name"},
		{"list", "--repository=repo", "--lock-wait=-1"}, {"list", "--repository=repo", "--lock-wait=1s"},
		{"list", "--repository=repo", "--passphrase-file="}, {"list", "--repository=repo", "--identity-file=key"},
		{"inspect", "--repository=repo", "--archive-name=name", "path"},
		{"list", "--repository=repo", "--dry-run"}, {"list", "--repository=repo", "--config=config.yaml"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), append([]string{"archive"}, args...), &stdout, &stderr, "test"); code != 3 || stdout.Len() != 0 ||
				strings.Contains(stderr.String(), "find Borg") {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
			}
		})
	}
	for _, args := range [][]string{{"archive", "--help"}, {"archive", "list", "--help"}, {"archive", "inspect", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), args, &stdout, &stderr, "test"); code != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("help code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
		}
	}
}

func TestArchiveFailures(t *testing.T) {
	path := archiveBorgEnvironment(t)
	for _, tt := range []struct {
		name   string
		data   string
		exit   string
		writer io.Writer
		code   int
	}{
		{name: "warning", data: `{"archives":[]}`, exit: "1", code: 1},
		{name: "modern warning", data: `{"archives":[]}`, exit: "100", code: 1},
		{name: "native failure", exit: "2", code: 2},
		{name: "malformed", data: `{"archives":[]`, code: 2},
		{name: "partial", data: `{"archives":[` + listArchiveJSON + `,false]}`, code: 2},
		{name: "oversized", data: `{"archives":[{"name":"` + strings.Repeat("x", archive.MaxRecordBytes) + `"}]}`, code: 2},
		{name: "stdout failure", data: `{"archives":[` + listArchiveJSON + `]}`, writer: brokenWriter{}, code: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("LIST_BORG_EXIT", tt.exit)
			if err := os.WriteFile(path, []byte(tt.data), 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			writer := tt.writer
			if writer == nil {
				writer = &stdout
			}
			if code := Run(t.Context(), []string{"archive", "list", "--repository=repo", "--json"}, writer, &stderr, "test"); code != tt.code ||
				strings.Contains(stderr.String(), "for usage") || strings.Contains(stdout.String(), "completed") {
				t.Fatalf("code = %d, stdout = %q, stderr = %q", code, &stdout, &stderr)
			}
		})
	}
	if err := os.WriteFile(path, []byte(`{"archives":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := Run(t.Context(), []string{"archive", "list", "--repository=repo"}, io.Discard, brokenWriter{}, "test"); code != 2 {
		t.Fatalf("stderr failure code = %d", code)
	}
}

func TestArchiveCancellation(t *testing.T) {
	path := archiveBorgEnvironment(t)
	if err := os.WriteFile(path, []byte(listFileJSON+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIST_BORG_WAIT", "yes")
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			var stderr bytes.Buffer
			writer := &archiveCancelWriter{cancel: func() { cancel(cancellation.Signal(signal)) }}
			done := make(chan int, 1)
			go func() {
				done <- Run(ctx, []string{"archive", "inspect", "--repository=repo", "--archive-name=archive", "--json"}, writer, &stderr, "test")
			}()
			select {
			case code := <-done:
				if code != 128+int(signal) {
					t.Fatalf("code = %d, stderr = %s", code, &stderr)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("listing did not stop")
			}
		})
	}
}

type archiveCancelWriter struct{ cancel func() }

func (w *archiveCancelWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}
