package prune

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	zero := 0
	got, err := Prepare(Options{
		Repository: "repo with spaces", ArchivePrefix: "-{hostname}-[ab]*",
		Retention:      Retention{Within: "2m", Secondly: -1, Minutely: 2, Hourly: 3, Daily: 4, Weekly: 5, Monthly: 6, Yearly: 7},
		PassphraseFile: "secret", LockWait: &zero,
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prune", "--lock-wait", "0", "--list", "--stats", "--glob-archives=-{hostname}-[ab]*-*",
		"--keep-within=2m", "--keep-secondly=-1", "--keep-minutely=2", "--keep-hourly=3",
		"--keep-daily=4", "--keep-weekly=5", "--keep-monthly=6", "--keep-yearly=7", "--", filepath.Join(dir, "repo with spaces")}
	if got.Selector != "-{hostname}-[ab]*-*" || !reflect.DeepEqual(got.Request.Args, want) ||
		got.Request.Dir != dir || got.Request.Access.PassphraseFile != filepath.Join(dir, "secret") || got.Request.Access.SSH {
		t.Fatalf("prepared = %+v, want args %q", got, want)
	}
}

func TestPreparePreviewAndSSH(t *testing.T) {
	dir := t.TempDir()
	got, err := Prepare(Options{
		Repository: "user@[::1]:repo", ArchivePrefix: "Documents", Retention: Retention{Daily: 0, Weekly: -2},
		PassphraseFile: "/secret", IdentityFile: "identity", DryRun: true,
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prune", "--lock-wait", "60", "--list", "--glob-archives=Documents-*",
		"--keep-weekly=-2", "--dry-run", "--", "user@[::1]:repo"}
	if !reflect.DeepEqual(got.Request.Args, want) || !got.Request.Access.SSH ||
		got.Request.Access.PassphraseFile != "/secret" || got.Request.Access.IdentityFile != filepath.Join(dir, "identity") {
		t.Fatalf("prepared = %+v", got)
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
		{"archive location", func(o *Options) { o.Repository = "repo::archive" }, "not an archive"},
		{"prefix", func(o *Options) { o.ArchivePrefix = "" }, "archive-prefix"},
		{"no policy", func(o *Options) { o.Retention = Retention{} }, "effective retention"},
		{"zero counts", func(o *Options) { o.Retention = Retention{Daily: 0, Yearly: 0} }, "effective retention"},
		{"interval despite count", func(o *Options) { o.Retention.Within = "0d" }, "keep-within"},
		{"lock", func(o *Options) { o.LockWait = &negative }, "nonnegative"},
		{"identity", func(o *Options) { o.IdentityFile = "identity" }, "SSH"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := Options{Repository: "repo", ArchivePrefix: "prefix", Retention: Retention{Daily: 1}}
			tt.change(&options)
			if _, err := Prepare(options, t.TempDir()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := Prepare(Options{Repository: "repo", ArchivePrefix: "prefix", Retention: Retention{Daily: 1}}, "relative"); err == nil {
		t.Fatal("accepted relative invocation directory")
	}
}

func TestIntervals(t *testing.T) {
	for _, interval := range []string{"1H", "2d", "3w", "4m", "5y", "01d", "+1d", "999999999999999999999y"} {
		if _, err := (Retention{Within: interval}).args(); err != nil {
			t.Errorf("rejected %q: %v", interval, err)
		}
	}
	for _, interval := range []string{"0H", "00d", "-1d", "1", "H", "1h", "1D", "1s", "1.5d", "1d2H", "0x10d", "invalid"} {
		if _, err := (Retention{Within: interval, Daily: 1}).args(); err == nil {
			t.Errorf("accepted %q", interval)
		}
	}
}
