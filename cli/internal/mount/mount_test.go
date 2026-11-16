package mount

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
)

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mount space")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	zero := 0
	request, err := Prepare(Options{Repository: "repo", ArchiveName: "{hostname}-before", PassphraseFile: "secret", LockWait: &zero}, dir, "mount space")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mount", "--foreground", "--lock-wait", "0", "--", filepath.Join(dir, "repo") + "::{hostname}-before", target}
	if !reflect.DeepEqual(request.Args, want) || request.Dir != dir || request.Access.PassphraseFile != filepath.Join(dir, "secret") {
		t.Fatalf("request = %#v", request)
	}
	request, err = Prepare(Options{Repository: "ssh://backup@host/./repo", ArchiveName: "name", IdentityFile: "key"}, dir, target)
	if err != nil || !request.Access.SSH || request.Access.IdentityFile != filepath.Join(dir, "key") ||
		!reflect.DeepEqual(request.Args, []string{"mount", "--foreground", "--lock-wait", "60", "--", "ssh://backup@host/./repo::name", target}) {
		t.Fatalf("SSH request=%#v error=%v", request, err)
	}
	negative := -1
	for _, options := range []Options{
		{}, {Repository: "repo"}, {Repository: "repo::archive", ArchiveName: "name"},
		{Repository: "repo", ArchiveName: "name", IdentityFile: "key"},
		{Repository: "repo", ArchiveName: "name", LockWait: &negative},
	} {
		if _, err := Prepare(options, dir, target); err == nil {
			t.Fatalf("accepted %#v", options)
		}
	}
	for _, path := range []string{"", "/", filepath.Join(dir, "absent")} {
		if _, err := Prepare(Options{Repository: "repo", ArchiveName: "name"}, dir, path); err == nil {
			t.Fatalf("accepted target %q", path)
		}
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Repository: "repo", ArchiveName: "name"}, dir, alias); err == nil {
		t.Fatal("accepted final symlink mountpoint")
	}
	if err := os.Chmod(target, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Repository: "repo", ArchiveName: "name"}, dir, target); err == nil {
		t.Fatal("accepted writable mountpoint")
	}
	if err := os.Chmod(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Options{Repository: "repo", ArchiveName: "name"}, dir, target); err == nil {
		t.Fatal("accepted nonempty mountpoint")
	}
}

func fakeRunner(t *testing.T) borg.Runner {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "borg")
	if out, err := exec.Command("go", "build", "-o", binary, "../borg/testdata/fakeborg").CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	t.Setenv("FAKE_VERSION", "borg 1.4.5")
	return borg.Runner{Executable: binary}
}

func TestVerifiedUnmount(t *testing.T) {
	runner := fakeRunner(t)
	request := borg.Request{Args: []string{"umount", "--", "/fixture"}}
	valid := []entry{{target: "/fixture", root: "/", filesystem: "fuse", source: "borgfs", uid: strconv.Itoa(os.Geteuid())}}
	for _, tt := range []struct {
		name, exit string
		before     []entry
		after      []entry
		readErr    error
		afterErr   error
		outcome    borg.Outcome
		called     bool
	}{
		{name: "success", before: valid, outcome: borg.Success, called: true},
		{name: "warning", before: valid, exit: "1", outcome: borg.Warning, called: true},
		{name: "helper failure", before: valid, after: valid, exit: "2", outcome: borg.Failure, called: true},
		{name: "false success", before: valid, after: valid, outcome: borg.Failure, called: true},
		{name: "unverifiable completion", before: valid, afterErr: errors.New("metadata gone"), outcome: borg.Failure, called: true},
		{name: "ambiguous", before: append(append([]entry{}, valid...), valid...), outcome: borg.Failure},
		{name: "missing", outcome: borg.Failure},
		{name: "metadata unavailable", readErr: errors.New("unavailable"), outcome: borg.Failure},
		{name: "unrelated", before: []entry{{target: "/fixture", filesystem: "ext4"}}, outcome: borg.Failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := filepath.Join(t.TempDir(), "calls")
			t.Setenv("FAKE_CALLS", record)
			t.Setenv("FAKE_EXIT", tt.exit)
			reads := 0
			result, err := unmount(t.Context(), runner, request, func() ([]entry, error) {
				reads++
				if reads == 1 {
					return tt.before, tt.readErr
				}
				return tt.after, tt.afterErr
			})
			if result.Outcome != tt.outcome || (err != nil) != (tt.outcome == borg.Failure) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			data, readErr := os.ReadFile(record)
			if (readErr == nil) != tt.called {
				t.Fatalf("unexpected execution: %s, %v", data, readErr)
			}
			if tt.called {
				var args []string
				if err := json.Unmarshal(data, &args); err != nil || !reflect.DeepEqual(args, request.Args) {
					t.Fatalf("arguments = %s, error=%v", data, err)
				}
			}
		})
	}
}

func TestForegroundRequestCancellation(t *testing.T) {
	runner := fakeRunner(t)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target")
			if err := os.Mkdir(target, 0700); err != nil {
				t.Fatal(err)
			}
			request, err := Prepare(Options{Repository: "repo", ArchiveName: "name"}, dir, target)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(dir, "process")
			t.Setenv("FAKE_TREE", base)
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			done := make(chan borg.Result, 1)
			go func() {
				// Only the fake executable sees the prepared mount request.
				result, _ := runner.Run(ctx, request)
				done <- result
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				_, err := os.Stat(base + ".child")
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("fixture did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case result := <-done:
				t.Fatalf("foreground request returned while its child was running: %#v", result)
			default:
			}
			cancel(cancellation.Signal(sig))
			select {
			case result := <-done:
				if result.Outcome != borg.Interrupted {
					t.Fatalf("result = %#v", result)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("owned process cleanup did not finish")
			}
			for _, role := range []string{"parent", "child"} {
				data, err := os.ReadFile(base + "." + role + ".signal")
				if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(int(sig)) {
					t.Fatalf("%s signal=%q, error=%v", role, data, err)
				}
				data, err = os.ReadFile(base + "." + role)
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(string(data))
				stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
				if err == nil && !strings.Contains(string(stat), ") Z ") {
					t.Fatalf("%s still executing: %s", role, stat)
				}
			}
		})
	}
}

func TestMissingFUSE(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); !errors.Is(err, os.ErrNotExist) {
		t.Skip("requires absent /dev/fuse; never opens a real mount")
	}
	result, err := Run(t.Context(), borg.Runner{Executable: "/must/not/run"}, borg.Request{Args: []string{"mount", "/unmounted-fixture"}})
	if result.Outcome != borg.Failure || err == nil || !strings.Contains(err.Error(), "FUSE unavailable") {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}
