package mount

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestRealBorgMountWithoutFUSE(t *testing.T) {
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("CAIRN_TEST_BORG is not set")
	}
	dir := t.TempDir()
	t.Setenv("BORG_BASE_DIR", filepath.Join(dir, "borg-state"))
	t.Setenv("BORG_FUSE_IMPL", "none")
	t.Setenv("BORG_PASSPHRASE", "")
	request, err := Prepare(Options{Repository: "never-opened-repo", ArchiveName: "fixture"}, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	request.Diagnostics = &diagnostics
	// Explicitly disable Borg's FUSE implementation: no mount can be attempted,
	// even on a test host that has /dev/fuse and working helpers.
	result, err := (borg.Runner{Executable: binary}).Run(t.Context(), request)
	if result.Outcome != borg.Failure || err == nil || !strings.Contains(diagnostics.String(), "no FUSE support") {
		t.Fatalf("result=%#v error=%v diagnostics=%s", result, err, &diagnostics)
	}
	if _, err := os.Stat(filepath.Join(dir, "never-opened-repo")); !os.IsNotExist(err) {
		t.Fatalf("unexpected repository access result: %v", err)
	}
}

func TestRealBorgUnmountHelperFailure(t *testing.T) {
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("CAIRN_TEST_BORG is not set")
	}
	absolute, err := exec.LookPath(binary)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err = filepath.Abs(absolute)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"failure", "missing"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "args")
			if name == "failure" {
				helper := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$HELPER_RECORD\"\necho 'fixture helper failure' >&2\nexit 2\n"
				if err := os.WriteFile(filepath.Join(dir, "fusermount"), []byte(helper), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir)
			t.Setenv("HELPER_RECORD", record)
			t.Setenv("BORG_BASE_DIR", filepath.Join(dir, "state"))
			target := filepath.Join(dir, "not a real mount")
			var diagnostics bytes.Buffer
			// Only the inert fixture helper can run. Both real helpers are
			// absent from PATH, including Borg's generic umount fallback.
			result, err := (borg.Runner{Executable: absolute}).Run(t.Context(), borg.Request{
				Args: []string{"umount", "--", target}, Diagnostics: &diagnostics,
			})
			if result.Outcome != borg.Failure || err == nil {
				t.Fatalf("result=%#v error=%v diagnostics=%s", result, err, &diagnostics)
			}
			if name == "failure" {
				data, err := os.ReadFile(record)
				if err != nil || string(data) != "-u\n"+target+"\n" || !strings.Contains(diagnostics.String(), "fixture helper failure") {
					t.Fatalf("helper args=%q error=%v diagnostics=%s", data, err, &diagnostics)
				}
			}
		})
	}
}
