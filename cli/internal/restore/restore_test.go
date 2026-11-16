package restore

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestUnpreparedPlan(t *testing.T) {
	result, err := (Plan{}).Run(t.Context(), borg.Runner{}, true, io.Discard)
	if err == nil || result.Outcome != borg.Failure {
		t.Fatal("unprepared restore plan did not fail closed")
	}
}

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	options := Options{Repository: "repo", ArchiveName: "snapshot", Destination: "restored"}
	plan, err := Prepare(options, dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.destination != filepath.Join(dir, "restored") || plan.request.Dir != dir ||
		plan.location != filepath.Join(dir, "repo")+"::snapshot" {
		t.Fatalf("plan = %+v", plan)
	}
	for _, name := range []string{"tree/file", "-option", "sh:literal", "nontext\xff", "name\n\x1b"} {
		options.Paths = []string{name}
		plan, err := Prepare(options, dir)
		if err != nil || len(plan.selectors) != 1 || plan.selectors[0] != "pp:"+name {
			t.Fatalf("literal %q: %+v, %v", name, plan, err)
		}
	}
}

func TestPrepareInvalid(t *testing.T) {
	dir := t.TempDir()
	base := Options{Repository: "repo", ArchiveName: "snapshot", Destination: "restored"}
	for _, tt := range []struct {
		name   string
		change func(*Options)
	}{
		{"repository", func(o *Options) { o.Repository = "" }},
		{"archive", func(o *Options) { o.ArchiveName = "" }},
		{"archive argument", func(o *Options) { o.ArchiveName = "x::other" }},
		{"archive template", func(o *Options) { o.ArchiveName = "{now}" }},
		{"repository template", func(o *Options) { o.Repository = "ssh://host/{now}" }},
		{"destination", func(o *Options) { o.Destination = "" }},
		{"root destination", func(o *Options) { o.Destination = "/" }},
		{"missing parent", func(o *Options) { o.Destination = "missing/restored" }},
		{"repository within destination", func(o *Options) { o.Destination = dir }},
		{"destination within repository", func(o *Options) { o.Repository = dir }},
		{"negative wait", func(o *Options) { n := -1; o.LockWait = &n }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := base
			tt.change(&options)
			if _, err := Prepare(options, dir); err == nil {
				t.Fatal("accepted invalid options")
			}
		})
	}
	for _, name := range []string{"", ".", "..", "../outside", "/absolute", "a/../outside", "a/./b", "a//b", "a/", "a\x00b"} {
		options := base
		options.Paths = []string{name}
		if _, err := Prepare(options, dir); err == nil {
			t.Errorf("accepted selector %q", name)
		}
	}
}

func TestDestinationChain(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "link/new", "link/../new"} {
		if _, err := Prepare(Options{Repository: "repo", ArchiveName: "a", Destination: name}, dir); err == nil {
			t.Errorf("accepted symlink destination %q", name)
		}
	}
	for _, name := range []string{"new/", "real/../new", "./new"} {
		if _, err := Prepare(Options{Repository: "repo", ArchiveName: "a", Destination: name}, dir); err != nil {
			t.Errorf("rejected safe destination %q: %v", name, err)
		}
	}
}

func listing(names ...string) string {
	var b strings.Builder
	for range names {
		fmt.Fprint(&b, "-\x00-rw-------\x00")
	}
	return b.String()
}

func TestListingSafety(t *testing.T) {
	destination := t.TempDir()
	for _, data := range []string{
		"p\x00hrw-------\x00", "?\x00?---------\x00",
		"a", "a\x00", "-\x00-rw-------",
		strings.Repeat("x", (1<<20)+1) + "\x00",
	} {
		if _, err := readListing(strings.NewReader(data)); err == nil {
			t.Errorf("accepted unsafe listing %.80q", data)
		}
	}
	if _, err := readListing(strings.NewReader(listing("a", "b"))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "a/../../outside", "/absolute", "a/../b", "a//b", "a/./b", "a/", ""} {
		if err := validPath(name, false); err == nil {
			t.Errorf("accepted unsafe path %q", name)
		}
		if name == "" || strings.HasSuffix(name, "/") {
			continue // Go's tar writer itself refuses these regular-file headers.
		}
		data := tarData(t, &tar.Header{Name: name, Typeflag: tar.TypeReg})
		if err := checkTar(bytes.NewReader(data), destination, []byte{'-'}); err == nil {
			t.Errorf("accepted unsafe archive path %q", name)
		}
	}
}

func TestExistingDestinationLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{outside, "missing", filepath.Join(outside, "file")} {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"link", "link/file"} {
			if err := checkDestination(dir, name); err == nil {
				t.Errorf("accepted destination symlink %q to %q", name, target)
			}
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(outside, "file"), filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := checkDestination(dir, "hardlink"); err == nil {
		t.Fatal("accepted existing external hardlink")
	}
	data, err := os.ReadFile(filepath.Join(outside, "file"))
	if err != nil || string(data) != "untouched" {
		t.Fatal("preflight changed outside file")
	}
}

func tarData(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range headers {
		h.Format = tar.FormatGNU
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTarSafety(t *testing.T) {
	for _, tt := range []struct {
		name    string
		list    string
		headers []*tar.Header
		valid   bool
	}{
		{"exact bytes", listing("nontext?", "nontext\xff"), []*tar.Header{
			{Name: "nontext?", Typeflag: tar.TypeReg}, {Name: "nontext\xff", Typeflag: tar.TypeReg},
		}, true},
		{"internal hardlink", listing("file", "link"), []*tar.Header{
			{Name: "file", Typeflag: tar.TypeReg}, {Name: "link", Linkname: "file", Typeflag: tar.TypeLink},
		}, true},
		{"external hardlink", listing("file"), []*tar.Header{
			{Name: "file", Linkname: "../outside", Typeflag: tar.TypeLink},
		}, false},
		{"unselected hardlink", listing("file"), []*tar.Header{
			{Name: "file", Linkname: "other", Typeflag: tar.TypeLink},
		}, false},
		{"type changed", listing("file"), []*tar.Header{
			{Name: "file", Typeflag: tar.TypeDir},
		}, false},
		{"traversal", listing("file"), []*tar.Header{
			{Name: "../file", Typeflag: tar.TypeReg},
		}, false},
		{"duplicate", listing("file"), []*tar.Header{
			{Name: "file", Typeflag: tar.TypeReg}, {Name: "file", Typeflag: tar.TypeReg},
		}, false},
		{"missing", listing("file"), nil, false},
		{"symlink itself is safe", "l\x00lrwxrwxrwx\x00", []*tar.Header{
			{Name: "link", Linkname: "/outside/nontext\xff", Typeflag: tar.TypeSymlink},
		}, true},
		{"symlink parent", "l\x00lrwxrwxrwx\x00" + listing("link/file"), []*tar.Header{
			{Name: "link", Linkname: "/outside", Typeflag: tar.TypeSymlink},
			{Name: "link/file", Typeflag: tar.TypeReg},
		}, false},
		{"symlink parent later", listing("link/file") + "l\x00lrwxrwxrwx\x00", []*tar.Header{
			{Name: "link/file", Typeflag: tar.TypeReg},
			{Name: "link", Linkname: "/outside", Typeflag: tar.TypeSymlink},
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			kinds, err := readListing(strings.NewReader(tt.list))
			if err != nil {
				t.Fatal(err)
			}
			err = checkTar(bytes.NewReader(tarData(t, tt.headers...)), dir, kinds)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
	if err := checkTar(strings.NewReader("not a tar stream"), t.TempDir(), []byte{'-'}); err == nil {
		t.Fatal("accepted malformed export")
	}
}
