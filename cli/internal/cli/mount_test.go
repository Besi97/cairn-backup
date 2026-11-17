package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

func TestMountCommandsValidateWithoutDependencies(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("BORG_REPO", "must-not-be-used")
	for _, args := range [][]string{
		{"mount"}, {"mount", "--repository=repo", "--destination=target"},
		{"mount", "--archive-name=name", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name"},
		{"mount", "--repository=repo", "--archive-name=name", "--destination="},
		{"mount", "--repository=repo", "--archive-name=name", "--lock-wait=-1", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--lock-wait=1s", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--passphrase-file=", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--identity-file=key", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--background", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--dry-run", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "-o", "allow_other", "--destination=target"},
		{"mount", "--repository=repo", "--archive-name=name", "--config=config", "--destination=target"},
		{"unmount"}, {"unmount", "--destination="}, {"unmount", "--destination=/"},
		{"unmount", "--force", "--destination=target"}, {"unmount", "--lazy", "--destination=target"},
		{"unmount", "--repository=repo", "--destination=target"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(t.Context(), append([]string{"archive"}, args...), &stdout, &stderr, "test")
			if code != 3 || stdout.Len() != 0 || strings.Contains(stderr.String(), "find Borg") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
			}
		})
	}
	for _, operation := range []string{"mount", "unmount"} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), []string{"archive", operation, "--help"}, &stdout, &stderr, "test"); code != 0 || !strings.Contains(stdout.String(), "--destination DIR") || stderr.Len() != 0 {
			t.Fatalf("help code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
	}
}

func TestUnmountRefusesUnrelatedPathsBeforeBorg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	for _, target := range []string{dir, filepath.Join(dir, "missing"), "/proc"} {
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), []string{"archive", "unmount", "--destination", target}, &stdout, &stderr, "test")
		if code != 2 || !strings.Contains(stderr.String(), "exact mountpoint") && !strings.Contains(stderr.String(), "same-user Borg") ||
			strings.Contains(stderr.String(), "find Borg") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
	}
}

func TestMountCommandsRejectArguments(t *testing.T) {
	t.Setenv("PATH", "")
	for _, operation := range []string{"mount", "unmount"} {
		for _, extra := range [][]string{
			{"unexpected"}, {"--", "-unexpected"}, {"one", "two"},
		} {
			var stdout, stderr bytes.Buffer
			args := []string{"archive", operation, "--destination=target"}
			if operation == "mount" {
				args = append(args, "--repository=repo", "--archive-name=name")
			}
			args = append(args, extra...)
			code := Run(t.Context(), args, &stdout, &stderr, "test")
			want := `for "cairn archive ` + operation + `"`
			if code != 3 || stdout.Len() != 0 || !strings.Contains(stderr.String(), want) ||
				!strings.Contains(stderr.String(), "unknown command") {
				t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, &stdout, &stderr)
			}
		}
	}
}

func TestMountCommandsRequireDestination(t *testing.T) {
	t.Setenv("PATH", "")
	for _, operation := range []string{"mount", "unmount"} {
		for _, extra := range [][]string{nil, {"--destination="}} {
			var stdout, stderr bytes.Buffer
			args := []string{"archive", operation}
			if operation == "mount" {
				args = append(args, "--repository=repo", "--archive-name=name")
			}
			args = append(args, extra...)
			code := Run(t.Context(), args, &stdout, &stderr, "test")
			if code != 3 || stdout.Len() != 0 ||
				!strings.Contains(stderr.String(), "--destination is required and must not be empty") {
				t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, &stdout, &stderr)
			}
		}
	}
}

func TestMountDependencyFailureBeforeSecrets(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); !errors.Is(err, os.ErrNotExist) {
		t.Skip("requires missing /dev/fuse")
	}
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "cairn.yaml"), []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	var stdout, stderr bytes.Buffer
	args := []string{"archive", "mount", "--repository=repo", "--archive-name=name", "--passphrase-file=/must-not-open", "--destination", target}
	code := Run(t.Context(), args, &stdout, &stderr, "test")
	if code != 2 || !strings.Contains(stderr.String(), "FUSE unavailable") || strings.Contains(stderr.String(), "open passphrase") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
	}
}

func TestUnmountFailureLifecycle(t *testing.T) {
	var events []lifecycle.Event
	ctx := lifecycle.WithObserver(t.Context(), func(event lifecycle.Event) error {
		events = append(events, event)
		return nil
	})
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, []string{"archive", "unmount", "--destination", t.TempDir()}, &stdout, &stderr, "test"); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	if len(events) != 4 || events[0].Kind != lifecycle.RunStarted || events[1].Kind != lifecycle.TaskStarted ||
		events[2].Kind != lifecycle.TaskFinished || events[3].Kind != lifecycle.RunFinished || events[3].Outcome != lifecycle.Failure {
		t.Fatalf("events=%+v", events)
	}
	for _, event := range events {
		if event.Operation != "unmount" || event.Repository != "" || event.Source != lifecycle.Manual {
			t.Fatalf("attribution=%+v", event)
		}
	}
}
