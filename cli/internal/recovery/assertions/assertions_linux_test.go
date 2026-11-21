package assertions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEvaluate(t *testing.T) {
	root := t.TempDir()
	contents := bytes.Repeat([]byte{0, 0xff, '\n', 'a'}, 20_000)
	writeFile(t, root, "data", contents)
	writeFile(t, root, "empty", nil)
	writeFile(t, root, "nested/literal \n\xff", []byte("x"))
	writeFile(t, root, "nested/more/deep", nil)
	makeDir(t, root, "empty-dir")
	must(t, os.Link(filepath.Join(root, "data"), filepath.Join(root, "nested/hardlink")))
	must(t, os.Symlink("missing", filepath.Join(root, "dangling")))
	must(t, os.Symlink("nested", filepath.Join(root, "in-tree")))
	must(t, os.Symlink(".", filepath.Join(root, "nested/loop")))
	must(t, unix.Mkfifo(filepath.Join(root, "nested/pipe"), 0600))
	outside := t.TempDir()
	writeFile(t, outside, "secret", contents)
	must(t, os.Symlink(outside, filepath.Join(root, "out-of-tree")))

	pass := Result{Status: Passed}
	wrongType := Result{Mismatch, WrongType}
	different := Result{Mismatch, DifferentValue}
	badParent := Result{EvaluationFailed, ParentNotDirectory}
	tests := []struct {
		name  string
		check Check
		want  Result
	}{
		{"root type", Check{Kind: PathType, Path: ".", Type: fs.ModeDir}, pass},
		{"regular type", Check{Kind: PathType, Path: "data"}, pass},
		{"literal name", Check{Kind: PathType, Path: "nested/literal \n\xff"}, pass},
		{"dangling symlink type", Check{Kind: PathType, Path: "dangling", Type: fs.ModeSymlink}, pass},
		{"FIFO type", Check{Kind: PathType, Path: "nested/pipe", Type: fs.ModeNamedPipe}, pass},
		{"wrong leaf type", Check{Kind: PathType, Path: "dangling"}, wrongType},
		{"missing leaf", Check{Kind: PathType, Path: "nested/missing"}, Result{Mismatch, MissingPath}},
		{"missing parent", Check{Kind: PathType, Path: "missing/file"}, Result{Mismatch, MissingPath}},
		{"regular parent", Check{Kind: PathType, Path: "data/child"}, badParent},
		{"internal symlink parent", Check{Kind: PathType, Path: "in-tree/hardlink"}, badParent},
		{"external symlink parent", Check{Kind: FileSHA256, Path: "out-of-tree/secret", Digest: sha256.Sum256(contents)}, badParent},
		{"size", Check{Kind: FileSize, Path: "data", Size: int64(len(contents))}, pass},
		{"size differs", Check{Kind: FileSize, Path: "data", Size: 1}, different},
		{"FIFO size", Check{Kind: FileSize, Path: "nested/pipe"}, wrongType},
		{"recursive count", Check{Kind: FileCount, Path: ".", Count: 5}, pass},
		{"subtree count", Check{Kind: FileCount, Path: "nested", Count: 3}, pass},
		{"empty count", Check{Kind: FileCount, Path: "empty-dir"}, pass},
		{"count differs", Check{Kind: FileCount, Path: "nested", Count: 2}, different},
		{"symlink count", Check{Kind: FileCount, Path: "in-tree"}, wrongType},
		{"exact bytes", Check{Kind: FileBytes, Path: "data", Bytes: contents}, pass},
		{"empty bytes", Check{Kind: FileBytes, Path: "empty"}, pass},
		{"expected prefix", Check{Kind: FileBytes, Path: "data", Bytes: contents[:len(contents)-1]}, different},
		{"actual prefix", Check{Kind: FileBytes, Path: "empty", Bytes: []byte("x")}, different},
		{"bytes differ at same length", Check{Kind: FileBytes, Path: "data", Bytes: bytes.Repeat([]byte("x"), len(contents))}, different},
		{"symlink bytes", Check{Kind: FileBytes, Path: "dangling"}, wrongType},
		{"digest", Check{Kind: FileSHA256, Path: "data", Digest: sha256.Sum256(contents)}, pass},
		{"empty digest", Check{Kind: FileSHA256, Path: "empty", Digest: sha256.Sum256(nil)}, pass},
		{"digest differs", Check{Kind: FileSHA256, Path: "data", Digest: sha256.Sum256(nil)}, different},
		{"FIFO digest", Check{Kind: FileSHA256, Path: "nested/pipe"}, wrongType},
	}
	var checks []Check
	for _, tc := range tests {
		checks = append(checks, tc.check)
	}
	got := Evaluate(t.Context(), root, checks)
	if len(got) != len(tests) {
		t.Fatalf("got %d results, want %d", len(got), len(tests))
	}
	for i, tc := range tests {
		if got[i] != tc.want {
			t.Errorf("%s = %+v, want %+v", tc.name, got[i], tc.want)
		}
	}
}

func TestInvalidChecks(t *testing.T) {
	var checks []Check
	for _, path := range []string{"", "/tmp/x", "..", "../x", "x/../y", "x/..", "./x", "x/.", "x//y", "x/", "x\x00"} {
		checks = append(checks, Check{Kind: PathType, Path: path})
	}
	checks = append(checks,
		Check{Path: "."},
		Check{Kind: PathType, Path: ".", Type: fs.ModeDir | 0700},
		Check{Kind: FileSize, Path: ".", Size: -1},
		Check{Kind: FileCount, Path: ".", Count: -1},
	)
	for i, got := range Evaluate(t.Context(), t.TempDir(), checks) {
		if got != (Result{EvaluationFailed, InvalidCheck}) {
			t.Errorf("invalid check %+v = %+v", checks[i], got)
		}
	}
}

func TestUnavailableRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file", nil)
	link := filepath.Join(root, "link")
	must(t, os.Symlink(root, link))
	for _, unavailable := range []string{"", filepath.Join(root, "missing"), filepath.Join(root, "file"), link + "/"} {
		got := Evaluate(t.Context(), unavailable, []Check{{Kind: FileCount, Path: "."}})
		if got[0] != (Result{EvaluationFailed, RootUnavailable}) {
			t.Errorf("root %q = %+v", unavailable, got)
		}
	}
}

func TestCountMultipleBatches(t *testing.T) {
	root := t.TempDir()
	for i := range 257 {
		writeFile(t, root, fmt.Sprintf("node-%d", i), nil)
	}
	got := Evaluate(t.Context(), root, []Check{{Kind: FileCount, Path: ".", Count: 257}})
	if got[0].Status != Passed {
		t.Fatal(got)
	}
}

func TestUnreadableTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file", nil)
	makeDir(t, root, "dir")
	writeFile(t, root, "dir/file", nil)
	must(t, os.Chmod(filepath.Join(root, "file"), 0000))
	dir := filepath.Join(root, "dir")
	must(t, os.Chmod(dir, 0000))
	t.Cleanup(func() { must(t, os.Chmod(dir, 0700)) })
	if f, err := os.Open(dir); err == nil {
		f.Close()
		t.Skip("process can read mode-000 directories")
	}
	checks := []Check{
		{Kind: FileSize, Path: "file"},
		{Kind: FileBytes, Path: "file"},
		{Kind: FileCount, Path: ".", Count: 1},
	}
	failed := Result{EvaluationFailed, FilesystemFailure}
	want := []Result{{Status: Passed}, failed, failed}
	if got := Evaluate(t.Context(), root, checks); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file", nil)
	checks := []Check{{Kind: FileBytes, Path: "file"}, {Kind: FileCount, Path: "."}}
	skipped := Result{NotEvaluated, Canceled}
	for _, tc := range []struct {
		name string
		poll int
		want []Result
	}{
		{"before first check", 1, []Result{skipped, skipped}},
		{"during content check", 4, []Result{{EvaluationFailed, Canceled}, skipped}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx = &cancelOnPoll{Context: ctx, cancel: cancel, remaining: tc.poll}
			if got := Evaluate(ctx, root, checks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

type cancelOnPoll struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func (ctx *cancelOnPoll) Err() error {
	ctx.remaining--
	if ctx.remaining == 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestOpenRegularUsesPinnedInode(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file", []byte("original"))
	dir, err := openRoot(root)
	must(t, err)
	defer dir.Close()
	n, err := lookup(t.Context(), dir, "file")
	must(t, err)
	defer n.file.Close()

	path := filepath.Join(root, "file")
	must(t, os.Remove(path))
	must(t, unix.Mkfifo(path, 0600))
	f, err := n.openRegular()
	must(t, err)
	defer f.Close()
	contents, err := io.ReadAll(f)
	must(t, err)
	if string(contents) != "original" {
		t.Fatalf("read %q, want original file", contents)
	}
}

func writeFile(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	makeDir(t, root, filepath.Dir(name))
	must(t, os.WriteFile(filepath.Join(root, name), contents, 0600))
}

func makeDir(t *testing.T, root, name string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(root, name), 0700))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
