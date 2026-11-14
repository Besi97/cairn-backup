package borg

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRepository(t *testing.T) {
	dir := t.TempDir()
	for _, location := range []string{"repo", "./repo:colon", "/repo", "file://./repo", "file:///repo",
		"ssh://user@host:2222/./repo", "ssh://user@[::1]:2222/~/repo", "user@host:repo", "[::1]:repo", "user@[::1]:repo"} {
		t.Run(location, func(t *testing.T) {
			ssh := strings.HasPrefix(location, "ssh://") || strings.HasPrefix(location, "user@") || strings.HasPrefix(location, "[")
			options := RepositoryOptions{Repository: location, PassphraseFile: "passphrase"}
			if ssh {
				options.IdentityFile = "identity"
			}
			got, err := PrepareRepository(options, dir)
			if err != nil {
				t.Fatal(err)
			}
			want := location
			if !ssh {
				path := strings.TrimPrefix(location, "file://")
				if !filepath.IsAbs(path) {
					path = filepath.Join(dir, path)
				}
				want = path
				if strings.HasPrefix(location, "file://") {
					want = "file://" + path
				}
			}
			if got.Location != want || got.Access.SSH != ssh || got.Access.PassphraseFile != filepath.Join(dir, "passphrase") {
				t.Fatalf("prepared = %+v, want location %q, SSH %v", got, want, ssh)
			}
			if ssh && got.Access.IdentityFile != filepath.Join(dir, "identity") {
				t.Fatalf("identity = %q", got.Access.IdentityFile)
			}
		})
	}
	got, err := PrepareRepository(RepositoryOptions{Repository: "/repo", PassphraseFile: "/secret"}, dir)
	if err != nil || got.Access.PassphraseFile != "/secret" || got.Access.IdentityFile != "" {
		t.Fatalf("absolute file = %+v: %v", got, err)
	}
}

func TestInvalidRepositoryInputs(t *testing.T) {
	for _, location := range []string{"", "::", "repo::archive", "host:repo::archive", "ssh://host/repo::archive",
		"user@[::1]:repo::archive", "file://"} {
		if _, err := PrepareRepository(RepositoryOptions{Repository: location}, t.TempDir()); err == nil {
			t.Errorf("accepted invalid repository: %q", location)
		}
	}
	if _, err := PrepareRepository(RepositoryOptions{Repository: "repo"}, "relative"); err == nil {
		t.Error("accepted relative invocation directory")
	}
	if _, err := PrepareRepository(RepositoryOptions{Repository: "repo", IdentityFile: "identity"}, t.TempDir()); err == nil {
		t.Error("accepted SSH identity for a local repository")
	}
}
