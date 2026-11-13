package backup

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrepare(t *testing.T) {
	invocation := t.TempDir()
	zero := 0
	now := time.Date(2026, 11, 14, 0, 2, 3, 123, time.FixedZone("east", 3*60*60))
	options := Options{
		Repository: "repo with spaces", ArchivePrefix: "{hostname}-Mixed_*",
		SourceRoot: "sources", Sources: []string{"./Documents/../Documents", "/absolute/source", "-source", "a,b", "$(false)"},
		Excludes: []string{"*.tmp", "sh:a,b/*", "--odd-pattern"}, PassphraseFile: "secret", LockWait: &zero,
	}
	got, err := Prepare(options, invocation, now)
	if err != nil {
		t.Fatal(err)
	}
	wantArchive := "{hostname}-Mixed_*-20261113T210203.000000123Z"
	wantArgs := []string{"create", "--lock-wait", "0", "--stats", "--progress", "--exclude=*.tmp", "--exclude=sh:a,b/*", "--exclude=--odd-pattern", "--", filepath.Join(invocation, options.Repository) + "::" + wantArchive}
	wantArgs = append(wantArgs, options.Sources...)
	if got.Archive != wantArchive || !reflect.DeepEqual(got.Request.Args, wantArgs) {
		t.Fatalf("prepared = %+v, want args %q", got, wantArgs)
	}
	if got.Request.Dir != filepath.Join(invocation, "sources") || got.Request.Access.PassphraseFile != filepath.Join(invocation, "secret") || got.Request.Access.SSH {
		t.Fatalf("request = %+v", got.Request)
	}
	if !reflect.DeepEqual(options.Sources, wantArgs[len(wantArgs)-len(options.Sources):]) {
		t.Fatal("source inputs mutated")
	}
}

func TestExactNameAndPreview(t *testing.T) {
	dir := t.TempDir()
	got, err := Prepare(Options{Repository: "/repo", ArchiveName: "{hostname}-Before Upgrade", Sources: []string{"."}, DryRun: true}, dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "--lock-wait", "60", "--dry-run", "--list", "--", "/repo::{hostname}-Before Upgrade", "."}
	if got.Archive != "{hostname}-Before Upgrade" || got.Request.Dir != dir || !reflect.DeepEqual(got.Request.Args, want) {
		t.Fatalf("prepared = %+v", got)
	}
}

func TestAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	root, passphrase, identity := "/source-root", "/passphrase", "/identity"
	got, err := Prepare(Options{
		Repository: "ssh://host/./repo", ArchiveName: "name", Sources: []string{"relative", "/absolute"},
		SourceRoot: root, PassphraseFile: passphrase, IdentityFile: identity,
	}, dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Request.Dir != root || got.Request.Access.PassphraseFile != passphrase || got.Request.Access.IdentityFile != identity {
		t.Fatalf("request = %+v", got.Request)
	}
}

func TestRepositoryLocations(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		location string
		want     string
		ssh      bool
	}{
		{"repo", filepath.Join(dir, "repo"), false},
		{"./repo:colon", filepath.Join(dir, "repo:colon"), false},
		{"/repo", "/repo", false},
		{"file://./repo", "file://" + filepath.Join(dir, "repo"), false},
		{"file:///repo", "file:///repo", false},
		{"ssh://user@host:2222/./repo", "ssh://user@host:2222/./repo", true},
		{"ssh://user@[::1]:2222/~/repo", "ssh://user@[::1]:2222/~/repo", true},
		{"user@host:repo", "user@host:repo", true},
		{"[::1]:repo", "[::1]:repo", true},
		{"user@[::1]:repo", "user@[::1]:repo", true},
	} {
		t.Run(tt.location, func(t *testing.T) {
			options := Options{Repository: tt.location, ArchiveName: "archive", Sources: []string{"source"}}
			if tt.ssh {
				options.IdentityFile = "identity"
			}
			got, err := Prepare(options, dir, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Request.Access.SSH != tt.ssh || got.Request.Args[len(got.Request.Args)-2] != tt.want+"::archive" {
				t.Fatalf("request = %+v", got.Request)
			}
			if tt.ssh && got.Request.Access.IdentityFile != filepath.Join(dir, "identity") {
				t.Fatalf("identity = %q", got.Request.Access.IdentityFile)
			}
		})
	}
}

func TestInvalidOptions(t *testing.T) {
	negative := -1
	for _, tt := range []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{"repository", func(o *Options) { o.Repository = "" }, "repository"},
		{"no name", func(o *Options) { o.ArchiveName = "" }, "exactly one"},
		{"both names", func(o *Options) { o.ArchivePrefix = "prefix" }, "exactly one"},
		{"no sources", func(o *Options) { o.Sources = nil }, "source path"},
		{"empty source", func(o *Options) { o.Sources = []string{""} }, "empty"},
		{"lock wait", func(o *Options) { o.LockWait = &negative }, "nonnegative"},
		{"local identity", func(o *Options) { o.IdentityFile = "id" }, "SSH"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{Repository: "repo", ArchiveName: "name", Sources: []string{"source"}}
			tt.change(&opts)
			if _, err := Prepare(opts, t.TempDir(), time.Time{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	for _, location := range []string{"::", "repo::archive", "host:repo::archive", "ssh://host/repo::archive", "user@[::1]:repo::archive", "file://"} {
		if _, _, err := repositoryLocation(location, t.TempDir()); err == nil {
			t.Errorf("accepted invalid repository input: %q", location)
		}
	}
}
