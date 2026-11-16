package cli

import (
	"archive/tar"
	"bytes"
	"context"
	encodingbinary "encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
	"golang.org/x/sys/unix"
)

func TestRealBorgRestore(t *testing.T) {
	for _, encryption := range []string{"none", "repokey"} {
		t.Run(encryption, func(t *testing.T) {
			binary := realBorgEnvironment(t)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			t.Chdir(t.TempDir())
			dir := mustGetwd(t)
			repository := filepath.Join(dir, "repo")
			const secret = "restore integration secret"
			borgOutput(t, ctx, binary, secret, "init", "--encryption="+encryption, repository)
			if err := os.MkdirAll("tree/empty", 0750); err != nil {
				t.Fatal(err)
			}
			names := []string{"file", "nontext?", "nontext\xff", "-option", "sh:literal", "line\n\x1b[31mname"}
			mtime := time.Unix(1_700_000_000, 123456789)
			for _, name := range names {
				file := filepath.Join("tree", name)
				if err := os.WriteFile(file, []byte("contents: "+name), 0640); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(file, mtime, mtime); err != nil {
					t.Fatal(err)
				}
			}
			xattrs := unix.Setxattr("tree/file", "user.cairn-test", []byte("preserved attribute"), 0) == nil
			acl := make([]byte, 4+5*8)
			encodingbinary.LittleEndian.PutUint32(acl, 2)
			for i, value := range [][3]uint32{{1, 6, ^uint32(0)}, {2, 4, uint32(os.Getuid() + 1)}, {4, 4, ^uint32(0)}, {16, 4, ^uint32(0)}, {32, 0, ^uint32(0)}} {
				offset := 4 + i*8
				encodingbinary.LittleEndian.PutUint16(acl[offset:], uint16(value[0]))
				encodingbinary.LittleEndian.PutUint16(acl[offset+2:], uint16(value[1]))
				encodingbinary.LittleEndian.PutUint32(acl[offset+4:], value[2])
			}
			acls := unix.Setxattr("tree/file", "system.posix_acl_access", acl, 0) == nil
			if err := os.Symlink("nontext\xff", "tree/link"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/outside/not-followed", "tree/external-link"); err != nil {
				t.Fatal(err)
			}
			if err := os.Link("tree/file", "tree/hardlink"); err != nil {
				t.Fatal(err)
			}
			borgOutput(t, ctx, binary, secret, "create", repository+"::snapshot", "tree")
			before := repositoryArchives(t, ctx, binary, repository, secret)
			if err := os.WriteFile("passphrase", []byte(secret+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BORG_PASSPHRASE", "wrong inherited secret")
			t.Setenv("BORG_REPO", "/must-not-be-used")
			args := []string{"restore", "--repository=repo", "--archive-name=snapshot", "--destination=new", "--passphrase-file=passphrase"}
			run := func(args []string, want int) {
				t.Helper()
				var stdout, stderr bytes.Buffer
				if code := Run(ctx, args, &stdout, &stderr, "test"); code != want {
					t.Fatalf("args=%q code=%d stdout=%s stderr=%s", args, code, &stdout, &stderr)
				}
				if strings.Contains(stdout.String()+stderr.String(), secret) {
					t.Fatal("secret leaked")
				}
			}
			run(append(append([]string(nil), args...), "--dry-run"), 0)
			if _, err := os.Stat("new"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preview created destination")
			}
			run(args, 0)
			for _, name := range names {
				got, err := os.ReadFile(filepath.Join("new/tree", name))
				if err != nil || string(got) != "contents: "+name {
					t.Fatalf("restored %q=%q error=%v", name, got, err)
				}
				info, err := os.Stat(filepath.Join("new/tree", name))
				if err != nil || info.Mode().Perm() != 0640 || !info.ModTime().Equal(mtime) {
					t.Fatalf("metadata %q: %+v %v", name, info, err)
				}
			}
			if target, err := os.Readlink("new/tree/link"); err != nil || target != "nontext\xff" {
				t.Fatalf("raw symlink target=%q error=%v", target, err)
			}
			a, _ := os.Stat("new/tree/file")
			b, _ := os.Stat("new/tree/hardlink")
			if !os.SameFile(a, b) {
				t.Fatal("hardlink relationship lost")
			}
			if xattrs {
				value := make([]byte, 128)
				n, err := unix.Getxattr("new/tree/file", "user.cairn-test", value)
				if err != nil || string(value[:n]) != "preserved attribute" {
					t.Fatalf("xattr=%q error=%v", value[:max(0, n)], err)
				}
			}
			if acls {
				value := make([]byte, 128)
				n, err := unix.Getxattr("new/tree/file", "system.posix_acl_access", value)
				if err != nil || !bytes.Equal(value[:n], acl) {
					t.Fatalf("ACL=%x error=%v", value[:max(0, n)], err)
				}
			}
			// A single hardlink slave must restore its contents even without its master.
			selected := []string{"restore", "--repository=repo", "--archive-name=snapshot", "--destination=selected", "--passphrase-file=passphrase", "tree/hardlink"}
			run(selected, 0)
			if got, err := os.ReadFile("selected/tree/hardlink"); err != nil || string(got) != "contents: file" {
				t.Fatalf("selected hardlink=%q error=%v", got, err)
			}
			if _, err := os.Stat("selected/tree/file"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unselected file restored")
			}
			// Display-colliding names remain distinct literal selectors.
			selected[3] = "--destination=nontext"
			selected[len(selected)-1] = "tree/nontext\xff"
			run(selected, 0)
			if _, err := os.Stat("nontext/tree/nontext?"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("lossy display name broadened restore selection")
			}
			if got, err := os.ReadFile("nontext/tree/nontext\xff"); err != nil || string(got) != "contents: nontext\xff" {
				t.Fatalf("raw selection=%q error=%v", got, err)
			}
			// Existing-directory consent authorizes replacement without deleting unrelated files.
			if err := os.MkdirAll("existing/tree", 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"existing/tree/file", "existing/unrelated"} {
				if err := os.WriteFile(name, []byte("original contents"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			existing := []string{"restore", "--repository=repo", "--archive-name=snapshot", "--destination=existing", "--passphrase-file=passphrase", "tree/file"}
			run(existing, 3)
			run(append(append([]string(nil), existing...), "--dry-run"), 0)
			if got, _ := os.ReadFile("existing/tree/file"); string(got) != "original contents" {
				t.Fatal("preview replaced existing file")
			}
			run(append(existing, "--allow-existing"), 0)
			if got, _ := os.ReadFile("existing/tree/file"); string(got) != "contents: file" {
				t.Fatal("authorized replacement did not happen")
			}
			if got, _ := os.ReadFile("existing/unrelated"); string(got) != "original contents" {
				t.Fatal("unrelated file changed")
			}
			if after := repositoryArchives(t, ctx, binary, repository, secret); !reflect.DeepEqual(before, after) {
				t.Fatal("restore changed archive inventory")
			}
			if encryption == "repokey" {
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if _, err := writer.WriteString(secret + "\n"); err != nil {
					t.Fatal(err)
				}
				writer.Close()
				command := exec.CommandContext(ctx, cairnBinary, "restore", "--repository=repo", "--archive-name=snapshot", "--destination=descriptor", "tree/file")
				for _, value := range os.Environ() {
					if !strings.HasPrefix(value, "BORG_PASSPHRASE=") {
						command.Env = append(command.Env, value)
					}
				}
				command.Env = append(command.Env, "BORG_PASSPHRASE_FD=3")
				command.ExtraFiles = []*os.File{reader}
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("descriptor restore failed: %v\n%s", err, output)
				}
				if strings.Contains(string(output), secret) {
					t.Fatal("descriptor credential leaked")
				}
			}
		})
	}
}

func TestRealBorgRestoreRejectsSymlinkTraversal(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	repository := filepath.Join(mustGetwd(t), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	if err := os.Mkdir("tree", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("tree/file", []byte("archive contents"), 0600); err != nil {
		t.Fatal(err)
	}
	borgOutput(t, ctx, binary, "", "create", repository+"::snapshot", "tree")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("outside untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("destination", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, "destination/tree"); err != nil {
		t.Fatal(err)
	}
	for _, preview := range []bool{false, true} {
		args := []string{"restore", "--repository=repo", "--archive-name=snapshot", "--destination=destination", "--allow-existing", "tree/file"}
		if preview {
			args = append(args, "--dry-run")
		}
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 || !strings.Contains(stderr.String(), "symlink") {
			t.Fatalf("code=%d stderr=%s", code, &stderr)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "file")); string(got) != "outside untouched" {
		t.Fatal("restore wrote outside destination")
	}
	// import-tar stores a symlink followed by a file below that symlink without
	// extracting either. Cairn must reject this archive before writing anything.
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	for _, header := range []*tar.Header{
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside, Mode: 0777},
		{Name: "link/file", Typeflag: tar.TypeReg, Mode: 0600},
	} {
		if err := w.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("unsafe.tar", data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	borgOutput(t, ctx, binary, "", "import-tar", repository+"::unsafe", "unsafe.tar")
	for _, preview := range []bool{false, true} {
		args := []string{"restore", "--repository=repo", "--archive-name=unsafe", "--destination=new"}
		if preview {
			args = append(args, "--dry-run")
		}
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 {
			t.Fatalf("unsafe archive code=%d stderr=%s", code, &stderr)
		}
		if _, err := os.Stat("new"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unsafe archive created destination")
		}
	}
}

func TestRealBorgRestoreFailuresAndLocking(t *testing.T) {
	binary := realBorgEnvironment(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	t.Chdir(t.TempDir())
	repository := filepath.Join(mustGetwd(t), "repo")
	borgOutput(t, ctx, binary, "", "init", "--encryption=none", repository)
	destination := filepath.Join(t.TempDir(), "new")
	args := []string{"restore", "--repository=" + repository, "--archive-name=missing", "--destination=" + destination}
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, args, &stdout, &stderr, "test"); code != 2 {
		t.Fatalf("missing archive code=%d stderr=%s", code, &stderr)
	}
	t.Run("locked", func(t *testing.T) {
		holdRepository(t, ctx, binary, repository)
		var stdout, stderr bytes.Buffer
		if code := Run(ctx, append(args, "--lock-wait=0"), &stdout, &stderr, "test"); code != 2 {
			t.Fatalf("lock timeout code=%d stderr=%s", code, &stderr)
		}
		waitCtx, stop := context.WithCancelCause(ctx)
		defer stop(nil)
		done := make(chan int, 1)
		go func() {
			done <- Run(waitCtx, append(args, "--lock-wait=30"), &stdout, &stderr, "test")
		}()
		awaitLockAttempt(t, repository)
		stop(cancellation.Signal(syscall.SIGTERM))
		select {
		case code := <-done:
			if code != 143 {
				t.Fatalf("cancel code=%d stderr=%s", code, &stderr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("restore did not stop")
		}
	})
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed restore created destination")
	}
}
