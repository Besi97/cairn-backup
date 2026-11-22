package assertions

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestCompareSample(t *testing.T) {
	restored, live := t.TempDir(), t.TempDir()
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("dir/%02d", i)
		writeFile(t, restored, name, []byte(name))
		writeFile(t, live, name, []byte(name))
	}
	must(t, os.Symlink("dir", filepath.Join(restored, "link")))
	must(t, os.Symlink(restored, filepath.Join(restored, "loop")))
	got := CompareSample(t.Context(), restored, live, 5)
	if got.Status != SamplePassed || got.Eligible != 40 || got.Selected != 5 || got.Unselected != 35 || got.Compared != 5 || got.Omitted != 0 {
		t.Fatal(got)
	}
	if again := CompareSample(t.Context(), restored, live, 5); !reflect.DeepEqual(got, again) {
		t.Fatalf("non-reproducible: %+v %+v", got, again)
	}
	// Changing every live file ensures every selected path differs.
	for i := 0; i < 40; i++ {
		writeFile(t, live, fmt.Sprintf("dir/%02d", i), []byte("different"))
	}
	got = CompareSample(t.Context(), restored, live, 5)
	if got.Status != SampleDifferent || got.Different != 5 || got.Compared != 5 {
		t.Fatal(got)
	}
}

func TestSampleSelectionStable(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	names := []string{"z", "a", "nested/c", "nested/b", "x"}
	for i, name := range names {
		writeFile(t, a, name, nil)
		writeFile(t, b, names[len(names)-1-i], nil)
	}
	want := append([]string(nil), names...)
	sort.Slice(want, func(i, j int) bool {
		return sampleLess(samplePath{want[i], sha256.Sum256([]byte(want[i]))}, samplePath{want[j], sha256.Sum256([]byte(want[j]))})
	})
	for _, root := range []string{a, b} {
		dir, err := openRoot(root)
		must(t, err)
		var selected []samplePath
		var eligible int64
		must(t, selectBelow(t.Context(), dir, ".", "", 3, &selected, &eligible))
		must(t, dir.Close())
		var got []string
		for _, p := range selected {
			got = append(got, p.name)
		}
		if eligible != 5 || !reflect.DeepEqual(got, want[:3]) {
			t.Fatalf("got %v of %d, want %v of 5", got, eligible, want[:3])
		}
	}
}

func TestSampleEmptyAndInvalid(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	must(t, os.Symlink(b, filepath.Join(a, "only-link")))
	got := CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Reason != SampleEmpty {
		t.Fatal(got)
	}
	for _, limit := range []int{0, -1, MaxSampleFiles + 1} {
		if got := CompareSample(t.Context(), a, b, limit); got.Status != SampleFailed || got.Reason != SampleInvalidLimit {
			t.Fatal(got)
		}
	}
}

func TestSampleLiveSymlinksAndMissing(t *testing.T) {
	a, b, outside := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, a, "dir/file", []byte("secret"))
	writeFile(t, outside, "file", []byte("secret"))
	must(t, os.Symlink(outside, filepath.Join(b, "dir")))
	got := CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Omitted != 1 || got.Compared != 0 {
		t.Fatal(got)
	}
	must(t, os.Remove(filepath.Join(b, "dir")))
	makeDir(t, b, "dir")
	must(t, os.Symlink(filepath.Join(outside, "file"), filepath.Join(b, "dir/file")))
	got = CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Omitted != 1 {
		t.Fatal(got)
	}
	must(t, os.Remove(filepath.Join(b, "dir/file")))
	got = CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Omitted != 1 {
		t.Fatal(got)
	}
	for _, roots := range [][2]string{{filepath.Join(a, "loop"), b}, {a, filepath.Join(b, "dir/file")}} {
		if got := CompareSample(t.Context(), roots[0], roots[1], 1); got.Status != SampleFailed || got.Reason != SampleRootUnavailable {
			t.Fatal(got)
		}
	}
}

func TestSampleUnreadable(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, a, "file", nil)
	writeFile(t, b, "file", nil)
	p := filepath.Join(b, "file")
	must(t, os.Chmod(p, 0000))
	if f, err := os.Open(p); err == nil {
		f.Close()
		t.Skip("process can read mode-000 files")
	}
	got := CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Omitted != 1 || got.Compared != 0 {
		t.Fatal(got)
	}
	must(t, os.Chmod(filepath.Join(a, "file"), 0000))
	must(t, os.Chmod(p, 0600))
	got = CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleIncomplete || got.Omitted != 1 {
		t.Fatal(got)
	}
	makeDir(t, a, "unreadable")
	must(t, os.Chmod(filepath.Join(a, "unreadable"), 0000))
	t.Cleanup(func() { must(t, os.Chmod(filepath.Join(a, "unreadable"), 0700)) })
	got = CompareSample(t.Context(), a, b, 1)
	if got.Status != SampleFailed || got.Reason != SampleFilesystemFailure {
		t.Fatal(got)
	}
}

func TestSampleCancellation(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeFile(t, a, "file", []byte("a"))
	writeFile(t, b, "file", []byte("a"))
	for _, poll := range []int{1, 4, 8} {
		ctx, cancel := context.WithCancel(t.Context())
		ctx = &cancelOnPoll{Context: ctx, cancel: cancel, remaining: poll}
		got := CompareSample(ctx, a, b, 1)
		cancel()
		if got.Status != SampleFailed || got.Reason != SampleCanceled {
			t.Fatalf("poll %d: %+v", poll, got)
		}
	}
}
