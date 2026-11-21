package workspace

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

func TestRealBorgWorkspace(t *testing.T) {
	binary := os.Getenv("CAIRN_TEST_BORG")
	if binary == "" {
		t.Skip("set CAIRN_TEST_BORG to exercise real Borg")
	}
	base, temp := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", temp)
	t.Setenv("BORG_BASE_DIR", filepath.Join(base, "borg"))
	t.Setenv("BORG_CACHE_DIR", filepath.Join(base, "cache"))
	t.Setenv("BORG_CONFIG_DIR", filepath.Join(base, "config"))
	t.Setenv("BORG_SECURITY_DIR", filepath.Join(base, "security"))
	t.Setenv("BORG_PASSPHRASE", "not-the-repository-key")
	runner := borg.Runner{Executable: binary}
	secret := "workspace-test-key"
	repo := borg.PreparedRepository{Location: filepath.Join(base, "repo"), Access: borg.AccessOptions{Passphrase: &secret}}
	runBorg := func(args ...string) {
		t.Helper()
		var output bytes.Buffer
		result, err := runner.Run(t.Context(), borg.Request{Args: args, Dir: base, Access: repo.Access, Diagnostics: &output})
		if err != nil || result.Outcome != borg.Success {
			t.Fatalf("%v: %+v, %v\n%s", args, result, err, &output)
		}
	}
	if err := os.Mkdir(filepath.Join(base, "tree"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"tree/name*": "selected contents", "tree/names": "whole only"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(base, "tree"), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(filepath.Join(base, "tree"), 0700); err != nil {
			t.Error(err)
		}
	})
	runBorg("init", "--encryption=repokey", repo.Location)
	runBorg("create", repo.Location+"::snapshot", "tree")

	for _, scope := range []struct {
		name  string
		paths []string
		whole bool
	}{
		{"selected", []string{"tree/name*"}, false},
		{"whole", nil, true},
	} {
		t.Run(scope.name, func(t *testing.T) {
			var output bytes.Buffer
			opts := Options{Repository: repo, ArchiveName: "snapshot", Paths: scope.paths, WholeArchive: scope.whole}
			called := false
			result, err := Run(t.Context(), runner, opts, &output, func(_ context.Context, path string) error {
				called = true
				if data, err := os.ReadFile(filepath.Join(path, "tree/name*")); err != nil || string(data) != "selected contents" {
					t.Fatalf("selected contents = %q, %v", data, err)
				}
				other, err := os.ReadFile(filepath.Join(path, "tree/names"))
				if scope.whole {
					if err != nil || string(other) != "whole only" {
						t.Fatalf("other contents = %q, %v", other, err)
					}
					info, err := os.Stat(filepath.Join(path, "tree"))
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != 0500 {
						t.Fatalf("restored native directory mode = %v", info.Mode())
					}
				} else if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("literal wildcard selector extracted a different file: %q, %v", other, err)
				}
				return nil
			})
			if !called || err != nil || result.Outcome != borg.Success {
				t.Fatalf("called = %v, result = %+v, error = %v\n%s", called, result, err, &output)
			}
			assertEmpty(t, temp)
		})
	}
}
