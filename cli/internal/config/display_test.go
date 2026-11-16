package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	input := `# secret-comment
version: 1
repositories:
  local:
    location: &repo ./relative-repo
    passphrase: &credential inline-secret
  remote:
    location: ssh://user@example.invalid/./repo
    passphrase_file: ./secret-file
    identity_file: ./identity-file
workflows:
  nightly:
    cron: "@daily"
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_name: "{hostname}"
            sources: [*repo, ".", *credential]
            lock_wait: 0
`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := Load(Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if *selected.Repositories["local"].Passphrase != "inline-secret" ||
		selected.Workflows["nightly"].Stages[0].Tasks[0].Sources[2] != "inline-secret" {
		t.Fatal("display redaction changed executable configuration")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	shown := selected.SanitizedYAML()
	for _, forbidden := range []string{"inline-secret", "secret-comment", "&repo", "*repo", "&credential", "*credential", dir} {
		if strings.Contains(shown, forbidden) {
			t.Fatalf("unexpected %q in display: %s", forbidden, shown)
		}
	}
	for _, want := range []string{"[redacted]", "./relative-repo", "./secret-file", "./identity-file", "ssh://user@example.invalid/./repo", "lock_wait: 0"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("missing %q in display: %s", want, shown)
		}
	}
	task := selected.SanitizedYAML("workflows", "nightly", "stages", "0", "tasks", "0")
	if !strings.Contains(task, "type: backup") || strings.Contains(task, "cron:") || strings.Contains(task, "source_root:") {
		t.Fatalf("wrong task section: %s", task)
	}
	for _, fields := range [][]string{{"missing"}, {"workflows", "nightly", "stages", "-1"}, {"workflows", "nightly", "stages", "no"}, {"workflows", "nightly", "stages", "1"}} {
		if got := selected.SanitizedYAML(fields...); got != "" {
			t.Fatalf("missing section %v = %q", fields, got)
		}
	}
}

func TestDisplayControlsAndLocations(t *testing.T) {
	for _, value := range []string{"\x1b[31m", "\x00\n\r\t", "\u009b31m", "\u202e", "\u2028"} {
		shown := DisplayText(value)
		if shown == value || strings.ContainsAny(shown, "\x1b\x00\n\r\t\u009b\u202e\u2028") {
			t.Fatalf("unsafe display %q", shown)
		}
	}
	for input, want := range map[string]string{
		"ssh://user:password@host/{now:%Y}/repo": "ssh://user:[redacted]@host/{now:%Y}/repo",
		"ssh://user@host:2222/./repo":            "ssh://user@host:2222/./repo",
		"ssh://user@[::1]:2222/~/repo":           "ssh://user@[::1]:2222/~/repo",
		"user@host:repo":                         "user@host:repo",
		"file:///my/repo":                        "file:///my/repo",
		"./my/repo":                              "./my/repo",
	} {
		if got := displayLocation(input); got != want {
			t.Errorf("displayLocation(%q) = %q, want %q", input, got, want)
		}
	}
	node := displayNode(map[string]any{"repositories": map[string]any{
		"bad\nname":  map[string]any{"location": "ssh://user:password@{hostname}/repo", "passphrase": ""},
		"bad\\nname": map[string]any{"location": "./other"},
	}}, nil)
	selected := Config{display: node}
	shown := selected.SanitizedYAML("repositories", "bad\nname")
	if strings.Contains(shown, "password") || strings.Contains(shown, "./other") || !strings.Contains(shown, "[redacted]") {
		t.Fatalf("unsafe or wrong section: %s", shown)
	}
}
