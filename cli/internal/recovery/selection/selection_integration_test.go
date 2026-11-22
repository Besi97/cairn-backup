package selection

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestRealBorgRecoverySelection(t *testing.T) {
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to test with real Borg")
	}
	dir := t.TempDir()
	t.Setenv("BORG_BASE_DIR", dir)
	t.Setenv("BORG_PASSPHRASE", "")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("PYTHONUTF8", "1")
	// Selection sets its own output encoding. Fixture commands don't depend
	// on stdout and can keep this setting.
	t.Setenv("PYTHONIOENCODING", "ascii")
	repo := filepath.Join(dir, "repo")
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%q: %v\n%s", args, err, output)
		}
	}
	run("init", "--encryption", "none", repo)
	create := func(name, clock string) {
		t.Helper()
		run("create", "--timestamp", "2024-01-01T00:00:"+clock, "--", repo+"::"+name, "data")
	}
	create("set[?]-checkpoint-source", "07")
	run("rename", "--", repo+"::set[?]-checkpoint-source", "set[?]-new.checkpoint.2")
	create("set[?]-pending.checkpoint", "06")
	create("setx-unrelated", "05")
	create("set[?]-b.checkpoint-final", "04")
	create("set[?]-a", "04") // same timestamp, insertion order opposite name order
	create("set[?]-source", "03")
	run("rename", "--", repo+"::set[?]-source", "set[?]-{{now}}\n-\xff")
	create("set[?]-{{now}}\n-?", "02") // collides with Borg's display name
	create("pending.checkpoint", "01")

	prepared, err := borg.PrepareRepository(borg.RepositoryOptions{Repository: repo}, dir)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Repository: prepared, Dir: dir}
	runner := borg.Runner{Executable: binary}
	resolve := func(selector Selector, want string) Candidate {
		t.Helper()
		var diagnostics bytes.Buffer
		got, result, err := Resolve(ctx, runner, options, selector, &diagnostics)
		if want == "" {
			if !errors.Is(err, ErrNotFound) || got != (Candidate{}) {
				t.Fatalf("%+v: got %+v, %v; want no match\n%s", selector, got, err, &diagnostics)
			}
		} else if err != nil || result.Outcome != borg.Success || got.Name != want || !validID(got.ArchiveID) || !validID(got.RepositoryID) {
			t.Fatalf("%+v: got %+v, %+v, %v; want %q\n%s", selector, got, result, err, want, &diagnostics)
		}
		return got
	}
	raw := resolve(Selector{Exact: "set[?]-{now}\n-\xff"}, "set[?]-{now}\n-\xff")
	literal := resolve(Selector{Exact: "set[?]-{now}\n-?"}, "set[?]-{now}\n-?")
	if raw.ArchiveID == literal.ArchiveID || raw.RepositoryID != literal.RepositoryID {
		t.Fatal("byte-exact names did not resolve to distinct archives in the same repository")
	}
	resolve(Selector{LatestPrefix: "set[?]-{now}\n-\xff"}, "set[?]-{now}\n-\xff")
	latest := resolve(Selector{LatestPrefix: "set[?]-"}, "set[?]-b.checkpoint-final")
	resolve(Selector{Exact: "set[?]-new.checkpoint.2"}, "set[?]-new.checkpoint.2")
	resolve(Selector{Exact: "set[?]-"}, "")
	resolve(Selector{LatestPrefix: "pending"}, "")
	resolve(Selector{LatestPrefix: "set*"}, "")

	if _, err := Revalidate(ctx, runner, options, latest, nil); err != nil {
		t.Fatalf("revalidate unchanged archive: %v", err)
	}
	run("rename", "--", repo+"::"+latest.Name, "renamed")
	resolve(Selector{Exact: latest.Name}, "")
	if _, err := Revalidate(ctx, runner, options, latest, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revalidate missing archive: %v", err)
	}
	create(latest.Name, "08")
	replaced := resolve(Selector{Exact: latest.Name}, latest.Name)
	if latest.ArchiveID == replaced.ArchiveID || latest.RepositoryID != replaced.RepositoryID {
		t.Fatal("manifest evidence did not distinguish replacement at the same name")
	}
	if _, err := Revalidate(ctx, runner, options, latest, nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("revalidate replaced archive: %v", err)
	}
}
