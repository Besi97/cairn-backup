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
		{"mount"}, {"mount", "--repository=repo", "target"},
		{"mount", "--archive-name=name", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--lock-wait=-1", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--lock-wait=1s", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--passphrase-file=", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--identity-file=key", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--background", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--dry-run", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "-o", "allow_other", "target"},
		{"mount", "--repository=repo", "--archive-name=name", "--config=config", "target"},
		{"unmount"}, {"unmount", "one", "two"}, {"unmount", "/"},
		{"unmount", "--force", "target"}, {"unmount", "--lazy", "target"},
		{"unmount", "--repository=repo", "target"},
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
		if code := Run(t.Context(), []string{"archive", operation, "--help"}, &stdout, &stderr, "test"); code != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("help code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
		}
	}
}

func TestUnmountRefusesUnrelatedPathsBeforeBorg(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	for _, target := range []string{dir, filepath.Join(dir, "missing"), "/proc"} {
		var stdout, stderr bytes.Buffer
		code := Run(t.Context(), []string{"archive", "unmount", target}, &stdout, &stderr, "test")
		if code != 2 || !strings.Contains(stderr.String(), "exact mountpoint") && !strings.Contains(stderr.String(), "same-user Borg") ||
			strings.Contains(stderr.String(), "find Borg") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, &stdout, &stderr)
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
	args := []string{"archive", "mount", "--repository=repo", "--archive-name=name", "--passphrase-file=/must-not-open", target}
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
	if code := Run(ctx, []string{"archive", "unmount", t.TempDir()}, &stdout, &stderr, "test"); code != 2 {
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
