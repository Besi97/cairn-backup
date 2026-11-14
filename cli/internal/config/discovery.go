package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func discover(options Options) (string, []byte, error) {
	var candidates []string
	if options.Explicit {
		if options.Path == "" {
			return "", nil, errors.New("--config must not be empty")
		}
		path := options.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(options.Directory, path)
		}
		candidates = []string{path}
	} else {
		base := options.XDGHome
		if !filepath.IsAbs(base) {
			if !filepath.IsAbs(options.Home) {
				return "", nil, errors.New("cannot resolve home directory for configuration discovery; use --config")
			}
			base = filepath.Join(options.Home, ".config")
		}
		system := options.SystemPath
		if system == "" {
			system = "/etc/cairn/config.yaml"
		}
		candidates = []string{filepath.Join(base, "cairn/config.yaml"), system}
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err == nil {
			return path, data, nil
		}
		if !options.Explicit && errors.Is(err, os.ErrNotExist) {
			// A dangling symlink is an existing, broken candidate, not absence.
			if _, linkErr := os.Lstat(path); errors.Is(linkErr, os.ErrNotExist) {
				continue
			}
		}
		return "", nil, fmt.Errorf("read configuration %s: %w", path, err)
	}
	return "", nil, fmt.Errorf("no configuration found; searched: %s", strings.Join(candidates, ", "))
}
