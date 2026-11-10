package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Besi97/cairn-backup/cli/internal/config"
)

func checkFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../internal/config/testdata/valid/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfigCheckCommand(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.yaml")
	invalid := filepath.Join(root, "invalid.yaml")
	malformed := filepath.Join(root, "malformed.yaml")
	unknown := filepath.Join(root, "unknown.yaml")
	duplicate := filepath.Join(root, "duplicate.yaml")
	for path, data := range map[string][]byte{
		valid:     checkFixture(t),
		invalid:   []byte("version: 2\nrepositories: {}\njobs: {}\n"),
		malformed: []byte("version: [LEAK"),
		unknown:   []byte("LEAK: true\n"),
		duplicate: []byte("version: 1\nversion: 1\n"),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := config.Environment{
		UserHomeDir:      func() (string, error) { return root, nil },
		Getenv:           func(string) string { return "" },
		SystemConfigPath: valid,
	}
	for _, test := range []struct {
		name   string
		args   []string
		code   int
		output string
	}{
		{"explicit", []string{"config", "check", "--config", valid}, 0, "Configuration is valid."},
		{"discovered", []string{"config", "check"}, 0, "Configuration is valid."},
		{"semantic", []string{"config", "check", "--config", invalid}, 3, "version"},
		{"malformed", []string{"config", "check", "--config", malformed}, 3, "invalid YAML"},
		{"unknown field", []string{"config", "check", "--config", unknown}, 3, "unknown field"},
		{"duplicate", []string{"config", "check", "--config", duplicate}, 3, "duplicate mapping key"},
		{"explicit missing no fallback", []string{"config", "check", "--config", filepath.Join(root, "missing")}, 3, "configuration file"},
		{"missing flag value", []string{"config", "check", "--config"}, 2, "usage:"},
		{"empty flag value", []string{"config", "check", "--config="}, 2, "usage:"},
		{"unknown flag", []string{"config", "check", "--LEAK"}, 2, "usage:"},
		{"extra argument", []string{"config", "check", "LEAK"}, 2, "usage:"},
		{"unknown command", []string{"LEAK"}, 2, "usage:"},
		{"missing command", nil, 2, "usage:"},
		{"incomplete command", []string{"config"}, 2, "usage:"},
		{"help", []string{"--help"}, 0, "usage:"},
		{"config help", []string{"config", "--help"}, 0, "usage:"},
		{"check help", []string{"config", "check", "--help"}, 0, "usage:"},
		{"version", []string{"--version"}, 0, "cairn "},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(test.args, &stdout, &stderr, env)
			if code != test.code {
				t.Fatalf("exit = %d, want %d: %s", code, test.code, stderr.String())
			}
			output := stdout.String()
			if code != 0 {
				if stdout.Len() != 0 {
					t.Errorf("unexpected stdout: %s", stdout.String())
				}
				output = stderr.String()
			} else if stderr.Len() != 0 {
				t.Errorf("unexpected stderr: %s", stderr.String())
			}
			if !strings.Contains(output, test.output) {
				t.Errorf("output = %q, want %q", output, test.output)
			}
			if strings.Contains(output, "LEAK") {
				t.Errorf("echoed sensitive input: %s", output)
			}
		})
	}
}

func TestHelpAndVersionDoNotLoadConfiguration(t *testing.T) {
	env := config.Environment{UserHomeDir: func() (string, error) { t.Fatal("loaded configuration"); return "", nil }}
	for _, args := range [][]string{{"--help"}, {"--version"}, {"config", "--help"}, {"config", "check", "--help"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, env); code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
	}
}

func TestConfigCheckBinaryWithoutBorg(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "cairn")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	// No binaries are available on PATH, including Borg or SSH. All configured
	// local paths are absent; timezone data still comes from the binary.
	fixture, err := filepath.Abs("../../internal/config/testdata/valid/scheduled.yaml")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "config", "check", "--config", fixture)
	command.Env = append(os.Environ(), "PATH="+root, "ZONEINFO="+root, "GOROOT="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("check: %v\n%s", err, output)
	}
	command = exec.Command(binary, "config", "check", "--config", filepath.Join(root, "missing.yaml"))
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
		t.Fatalf("invalid configuration exit: %v", err)
	}
	command = exec.Command(binary, "config", "check", "--unknown")
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Fatalf("usage exit: %v", err)
	}
}
