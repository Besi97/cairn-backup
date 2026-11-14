package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscovery(t *testing.T) {
	for _, mode := range []string{"explicit", "xdg", "home", "empty xdg", "relative xdg", "system"} {
		t.Run(mode, func(t *testing.T) {
			options := testOptions(t)
			user := filepath.Join(options.Home, ".config/cairn/config.yaml")
			selected := user
			writeConfig(t, options.SystemPath, minimal)
			switch mode {
			case "explicit":
				options.Explicit, options.Path = true, "explicit.yaml"
				selected = filepath.Join(options.Directory, options.Path)
				writeConfig(t, user, "invalid")
			case "xdg":
				options.XDGHome = filepath.Join(options.Directory, "xdg")
				selected = filepath.Join(options.XDGHome, "cairn/config.yaml")
				writeConfig(t, user, "invalid")
			case "relative xdg":
				options.XDGHome = "ignored"
			case "system":
				selected = options.SystemPath
			}
			writeConfig(t, selected, minimal)
			config, err := Load(options)
			if err != nil {
				t.Fatal(err)
			}
			if config.Path != selected || config.Directory != filepath.Dir(selected) {
				t.Fatalf("selected %q, directory %q", config.Path, config.Directory)
			}
		})
	}
}

func TestDiscoveryErrorsDoNotFallBack(t *testing.T) {
	for _, mode := range []string{"empty explicit", "missing explicit", "invalid explicit", "invalid user", "directory user", "dangling symlink", "unreadable user"} {
		t.Run(mode, func(t *testing.T) {
			options := testOptions(t)
			user := filepath.Join(options.Home, ".config/cairn/config.yaml")
			writeConfig(t, options.SystemPath, minimal)
			switch mode {
			case "empty explicit":
				options.Explicit = true
			case "missing explicit":
				options.Explicit, options.Path = true, "missing.yaml"
			case "invalid explicit":
				options.Explicit, options.Path = true, "invalid.yaml"
				writeConfig(t, filepath.Join(options.Directory, options.Path), "invalid")
				writeConfig(t, user, minimal)
			case "invalid user":
				writeConfig(t, user, "version: 2")
			case "directory user":
				if err := os.MkdirAll(user, 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.MkdirAll(filepath.Dir(user), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing.yaml", user); err != nil {
					t.Fatal(err)
				}
			case "unreadable user":
				writeConfig(t, user, minimal)
				if err := os.Chmod(user, 0000); err != nil {
					t.Fatal(err)
				}
				if _, err := os.ReadFile(user); err == nil {
					t.Skip("user can read mode-000 files")
				}
			}
			if _, err := Load(options); err == nil {
				t.Fatal("expected selection error, not fallback")
			}
		})
	}
}

func TestMissingConfigurationListsCandidates(t *testing.T) {
	options := testOptions(t)
	_, err := Load(options)
	for _, want := range []string{"searched", filepath.Join(options.Home, ".config/cairn/config.yaml"), options.SystemPath} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v; want %q", err, want)
		}
	}
}

func TestNoMerging(t *testing.T) {
	options := testOptions(t)
	writeConfig(t, filepath.Join(options.Home, ".config/cairn/config.yaml"), minimal)
	writeConfig(t, options.SystemPath, withTask("{type: compact, repository: local}"))
	config, err := Load(options)
	if err != nil || len(config.Repositories) != 0 || len(config.Workflows) != 0 {
		t.Fatalf("configuration = %#v, error = %v", config, err)
	}
}

func TestDiscoveryWithoutHome(t *testing.T) {
	options := testOptions(t)
	options.Home = ""
	if _, err := Load(options); err == nil {
		t.Fatal("expected unresolved user config error")
	}
	options.XDGHome = filepath.Join(options.Directory, "xdg")
	writeConfig(t, options.SystemPath, minimal)
	if _, err := Load(options); err != nil {
		t.Fatal(err)
	}
}
