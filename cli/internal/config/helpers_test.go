package config

import (
	"os"
	"path/filepath"
	"testing"
)

const minimal = "version: 1\nrepositories: {}\nworkflows: {}\n"

func testOptions(t *testing.T) Options {
	t.Helper()
	base := t.TempDir()
	return Options{
		Directory: base, Home: filepath.Join(base, "home"),
		SystemPath: filepath.Join(base, "etc/cairn/config.yaml"),
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func loadText(t *testing.T, text string) (*Config, error) {
	t.Helper()
	options := testOptions(t)
	options.Explicit, options.Path = true, "config.yaml"
	writeConfig(t, filepath.Join(options.Directory, options.Path), text)
	return Load(options)
}

func withTask(task string) string {
	return "version: 1\nrepositories:\n  local: {location: ./repo}\nworkflows:\n  manual:\n    stages:\n      - tasks:\n          - " + task + "\n"
}

func firstTask(config *Config) Task {
	return config.Workflows["manual"].Stages[0].Tasks[0]
}
