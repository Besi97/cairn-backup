package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCompletion(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), append([]string{"__complete"}, args...), &stdout, &stderr, "test")
	return code, stdout.String(), stderr.String()
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), []string{"completion", shell}, &stdout, &stderr, "test"); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, &stderr)
			}
			if !strings.Contains(stdout.String(), "cairn") {
				t.Fatalf("completion script does not mention cairn: %q", stdout.String())
			}
		})
	}
}

func TestCompletionCommandsAndFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"root commands", []string{""}, "backup"},
		{"backup flags", []string{"backup", "--"}, "--repository"},
		{"prune flags", []string{"prune", "--"}, "--archive-prefix"},
		{"compact flags", []string{"compact", "--"}, "--repository"},
		{"workflow commands", []string{"workflow", ""}, "run"},
		{"config commands", []string{"config", ""}, "check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCompletion(t, tt.args...)
			if code != 0 || !strings.Contains(stdout, tt.want) {
				t.Fatalf("code=%d stdout=%q stderr=%q; want %q", code, stdout, stderr, tt.want)
			}
		})
	}
}

func TestCompletionPathDirectives(t *testing.T) {
	for _, tt := range []struct {
		name      string
		args      []string
		directive string
	}{
		{"local repository", []string{"backup", "--repository", "some/path"}, ":0\n"},
		{"local repository at sign", []string{"backup", "--repository", "./some/user@host/repo"}, ":0\n"},
		{"local repository colon", []string{"backup", "--repository", "./some/with:colon/repo"}, ":0\n"},
		{"file repository URL", []string{"backup", "--repository", "file://./some/repo"}, ":0\n"},
		{"remote repository", []string{"backup", "--repository", "ssh://user@host/repo"}, ":4\n"},
		{"scp repository", []string{"backup", "--repository", "user@host:/repo"}, ":4\n"},
		{"archive template", []string{"backup", "--archive-name", "snapshot"}, ":4\n"},
		{"source directory", []string{"backup", "--source-root", "some/path"}, ":16\n"},
		{"source path", []string{"backup", "some/path"}, ":0\n"},
		{"credential file", []string{"compact", "--passphrase-file", "some/path"}, ":0\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runCompletion(t, tt.args...)
			if code != 0 || !strings.HasSuffix(stdout, tt.directive) {
				t.Fatalf("code=%d stdout=%q stderr=%q; want directive %q", code, stdout, stderr, tt.directive)
			}
		})
	}
}

func TestBackupSourceCompletionUsesSourceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file-one", "file-two", ".hidden"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "subdir", "file-nested"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCompletion(t, "backup", "--source-root", root, "file-")
	if code != 0 || stdout != "file-one\nfile-two\n:4\n" {
		t.Fatalf("source-root completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCompletion(t, "backup", "--source-root", root, "subdir/fi")
	if code != 0 || stdout != "subdir/file-nested\n:4\n" {
		t.Fatalf("nested source-root completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCompletion(t, "backup", "--source-root", root, "./file-")
	if code != 0 || stdout != "./file-one\n./file-two\n:4\n" {
		t.Fatalf("preserved path prefix: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCompletion(t, "backup", "--source-root", root, "sub")
	if code != 0 || stdout != "subdir/\n:6\n" {
		t.Fatalf("directory completion directive: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	if err := os.Symlink("subdir", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCompletion(t, "backup", "--source-root", root, "linked")
	if code != 0 || stdout != "linked/\n:6\n" {
		t.Fatalf("symlinked directory completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCompletion(t, "backup", "--source-root", root, ".")
	if code != 0 || stdout != ".hidden\n:4\n" {
		t.Fatalf("dotfile source-root completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	relativeRoot, err := filepath.Rel(mustGetwd(t), root)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCompletion(t, "backup", "--source-root", relativeRoot, "subdir/fi")
	if code != 0 || stdout != "subdir/file-nested\n:4\n" {
		t.Fatalf("relative source-root completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWorkflowNameCompletion(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	configText := `version: 1
repositories:
  local: {location: ./unused}
workflows:
  beta:
    stages:
      - tasks: [{type: compact, repository: local}]
  alpha-two:
    stages:
      - tasks: [{type: compact, repository: local}]
  alpha-one:
    stages:
      - tasks: [{type: compact, repository: local}]
`
	path := writeWorkflowConfig(t, configText)
	code, stdout, stderr := runCompletion(t, "workflow", "run", "--config", path, "alpha")
	if code != 0 || stdout != "alpha-one\nalpha-two\n:4\n" {
		t.Fatalf("explicit config completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	configPath := filepath.Join(home, "cairn", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(strings.ReplaceAll(configText, "alpha-", "discovered-")), 0600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCompletion(t, "workflow", "run", "discovered")
	if code != 0 || stdout != "discovered-one\ndiscovered-two\n:4\n" {
		t.Fatalf("discovered config completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = runCompletion(t, "workflow", "run", "--config", path, "alpha-one", "")
	if code != 0 || stdout != ":4\n" {
		t.Fatalf("after workflow name: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	invalid := writeWorkflowConfig(t, "not: [valid")
	code, stdout, stderr = runCompletion(t, "workflow", "run", "--config", invalid, "alpha")
	if code != 0 || stdout != ":4\n" || strings.Contains(stderr, "Error:") {
		t.Fatalf("invalid config completion: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
