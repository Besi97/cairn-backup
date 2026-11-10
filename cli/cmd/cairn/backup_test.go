package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

// fakeBorgFor installs a borg script on PATH. It answers --version and exits
// with the given status for other invocations.
func fakeBorgFor(t *testing.T, exit string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 'borg 1.4.5'; exit 0 ;; esac\nexit " + exit + "\n"
	if err := os.WriteFile(filepath.Join(dir, "borg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func backupFixture(t *testing.T) (config.Environment, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data", "service"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.yaml")
	document := "version: 1\nrepositories:\n  local:\n    location: ./repository\njobs:\n  service:\n    repository: local\n    source_root: ./data\n    sources: [service]\n    archive_prefix: service\n"
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Environment{UserHomeDir: func() (string, error) { return root, nil }, Getenv: func(string) string { return "" }}, path
}

func TestBackupCommand(t *testing.T) {
	env, path := backupFixture(t)
	for _, test := range []struct {
		name string
		args []string
		exit string
		code int
		out  string
	}{
		{"success", []string{"backup", "--job", "service", "--config", path}, "0", 0, "completed."},
		{"warning", []string{"backup", "--job", "service", "--config", path}, "1", 1, "completed with warnings"},
		{"borg failure", []string{"backup", "--job", "service", "--config", path}, "2", 4, "failed"},
		{"missing job", []string{"backup", "--config", path}, "0", 2, "usage:"},
		{"empty job", []string{"backup", "--job=", "--config", path}, "0", 2, "usage:"},
		{"empty config", []string{"backup", "--job", "service", "--config="}, "0", 2, "usage:"},
		{"positional argument", []string{"backup", "--job", "service", "LEAK"}, "0", 2, "usage:"},
		{"unknown flag", []string{"backup", "--job", "service", "--LEAK"}, "0", 2, "usage:"},
		{"dry run rejected", []string{"backup", "--job", "service", "--dry-run", "--config", path}, "0", 2, "not available yet"},
		{"unknown job", []string{"backup", "--job", "absent", "--config", path}, "0", 2, "not configured"},
		{"invalid configuration", []string{"backup", "--job", "service", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, "0", 3, "configuration file"},
		{"help", []string{"backup", "--help"}, "0", 0, "usage:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fakeBorgFor(t, test.exit)
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr, env)
			if code != test.code {
				t.Fatalf("exit = %d, want %d: stdout=%q stderr=%q", code, test.code, stdout.String(), stderr.String())
			}
			output := stdout.String() + stderr.String()
			if !strings.Contains(output, test.out) {
				t.Fatalf("output = %q, want %q", output, test.out)
			}
			if strings.Contains(output, "LEAK") {
				t.Fatalf("echoed sensitive argument: %s", output)
			}
		})
	}
}

func TestBackupMissingBorgExitsWithFailure(t *testing.T) {
	env, path := backupFixture(t)
	t.Setenv("PATH", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"backup", "--job", "service", "--config", path}, &stdout, &stderr, env); code != 4 {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
}
