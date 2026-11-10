package backup

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// TestRealBorgRoundTrip backs up to a local repository with the real Borg
// executable and extracts it with native Borg. It is skipped when borg is not
// on PATH.
func TestRealBorgRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("borg"); err != nil {
		t.Skip("borg is not installed")
	}
	root := t.TempDir()
	t.Setenv("BORG_BASE_DIR", filepath.Join(root, "borg-base"))

	files := map[string]string{
		"service/data/alpha.txt":     "alpha contents\n",
		"service/data/nested/b.bin":  "\x00\x01\x02 binary",
		"service/config/app.conf":    "key = value\n",
		"service/data/-leading-dash": "option-like name",
	}
	for name, contents := range files {
		path := filepath.Join(root, "inputs", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "passphrase"), []byte("round-trip-passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document := `version: 1
repositories:
  local:
    location: ./repository
    passphrase_file: ./passphrase
jobs:
  service:
    repository: local
    source_root: ./inputs
    sources: [service/data, service/config]
    archive_prefix: service
`
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadValidated(configPath)
	if err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(root, "repository")
	borg := func(dir string, args ...string) ([]byte, error) {
		command := exec.Command("borg", args...)
		command.Dir = dir
		command.Env = append(os.Environ(), "BORG_PASSPHRASE=round-trip-passphrase")
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		err := command.Run()
		return output.Bytes(), err
	}
	if out, err := borg(root, "init", "--encryption=repokey-blake2", repository); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	status, err := Run(context.Background(), cfg, "service", io.Discard, io.Discard)
	if err != nil || status != Succeeded {
		t.Fatalf("backup status = %v, err = %v", status, err)
	}

	list, err := borg(root, "list", "--short", repository)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, list)
	}
	archives := strings.Fields(string(list))
	if len(archives) != 1 || !strings.HasPrefix(archives[0], "service-") {
		t.Fatalf("archives = %q", archives)
	}

	restore := filepath.Join(root, "restore")
	if err := os.MkdirAll(restore, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := borg(restore, "extract", repository+"::"+archives[0]); err != nil {
		t.Fatalf("extract: %v\n%s", err, out)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(restore, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}
