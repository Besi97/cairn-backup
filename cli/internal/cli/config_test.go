package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigCheck(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.yaml")
	text := `version: 1
repositories:
  remote: {location: "backup@host:repo", passphrase_file: ./missing-secret, identity_file: ./missing-key}
workflows:
  manual:
    stages:
      - tasks:
          - {type: backup, repository: remote, archive_prefix: "{hostname}", source_root: ./missing-source, sources: [.]}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	// No Borg binary is available, and nothing named by the config exists.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "broken-default"))
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"config", "check", "--config", path}, 0, "Configuration valid: " + path},
		{[]string{"config", "check", "--config", ""}, 3, "--config must not be empty"},
		{[]string{"config", "check", "--config", path + ".missing"}, 3, "read configuration"},
		{[]string{"config", "check", "--config", path, "extra"}, 3, "unknown command"},
		{[]string{"config", "check", "--help"}, 0, "without executing Borg"},
		{[]string{"backup", "--config", path}, 3, "unknown flag"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, stderr bytes.Buffer
			code := Run(t.Context(), tt.args, &out, &stderr, "test")
			if code != tt.code || !strings.Contains(out.String()+stderr.String(), tt.want) {
				t.Fatalf("code=%d out=%q err=%q; want %d %q", code, &out, &stderr, tt.code, tt.want)
			}
		})
	}
}

func TestConfigDiscoveryAndRedaction(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	path := filepath.Join(base, "cairn/config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"version: 1\nrepositories: {}\nworkflows: {}\n",
		"version: [never-print-this",
		"version: 1\nrepositories: {repo: {location: ./repo, passphrase: [never-print-this]}}\nworkflows: {}",
	} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := Run(t.Context(), []string{"config", "check"}, &out, &stderr, "test")
		if strings.Contains(text, "never-print-this") {
			if code != 3 || strings.Contains(out.String()+stderr.String(), "never-print-this") {
				t.Fatalf("code=%d out=%q err=%q", code, &out, &stderr)
			}
		} else if code != 0 {
			t.Fatalf("code=%d err=%q", code, &stderr)
		}
	}
}

func TestUnrelatedInvalidConfigurationIsIgnored(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	if err := os.MkdirAll(filepath.Join(base, "cairn"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "cairn/config.yaml"), []byte("never-load-this: ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"--help"}, {"--version"}, {"config", "check", "--help"},
		{"backup", "--repository", "./repo", "--archive-prefix", "docs", "."},
		{"prune", "--repository", "./repo", "--archive-prefix", "docs", "--keep-daily", "1"},
		{"compact", "--repository", "./repo"},
	} {
		var out, stderr bytes.Buffer
		code := Run(t.Context(), args, &out, &stderr, "test")
		if code == 3 || strings.Contains(stderr.String(), "configuration") || strings.Contains(stderr.String(), "never-load-this") {
			t.Fatalf("args=%v code=%d err=%q", args, code, &stderr)
		}
	}
}
