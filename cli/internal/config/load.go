package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// DefaultSystemConfig is the system-wide configuration path.
const DefaultSystemConfig = "/etc/cairn/config.yaml"

// Environment supplies the process environment used during discovery and path
// expansion. A zero value falls back to the current process.
type Environment struct {
	Getenv           func(string) string
	UserHomeDir      func() (string, error)
	SystemConfigPath string
}

// DefaultEnvironment returns an Environment backed by the current process.
func DefaultEnvironment() Environment {
	return Environment{}
}

func (e Environment) getenv(key string) string {
	if e.Getenv != nil {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

func (e Environment) homeDir() (string, error) {
	if e.UserHomeDir != nil {
		return e.UserHomeDir()
	}
	return os.UserHomeDir()
}

func (e Environment) systemConfig() string {
	if e.SystemConfigPath != "" {
		return e.SystemConfigPath
	}
	return DefaultSystemConfig
}

// Load loads configuration using the default environment. See
// Environment.Load.
func Load(explicit string) (*Config, error) {
	return DefaultEnvironment().Load(explicit)
}

// Load discovers, decodes, and resolves a configuration. An explicit path is
// authoritative: if it is missing or broken, loading fails without looking for
// another file. Otherwise the user configuration is used when present, with the
// system configuration as the final fallback.
func (e Environment) Load(explicit string) (*Config, error) {
	return e.load(explicit, false)
}

// LoadValidated loads, validates, and resolves configuration. Execution preflight
// and agent startup must use this entry point and reject errors before work starts.
// It reads the configuration file and may load timezone data, but does not
// access referenced files or repositories.
func LoadValidated(explicit string) (*Config, error) {
	return DefaultEnvironment().LoadValidated(explicit)
}

// LoadValidated uses this environment for the shared validated loading path.
func (e Environment) LoadValidated(explicit string) (*Config, error) {
	return e.load(explicit, true)
}

func (e Environment) load(explicit string, validate bool) (*Config, error) {
	path, err := e.Discover(explicit)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read configuration %s: %w", path, err)
	}

	config, err := decode(path, data)
	if err != nil {
		return nil, err
	}
	if validate {
		if err := config.Validate(); err != nil {
			return nil, fmt.Errorf("configuration %s: %w", path, err)
		}
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve configuration path %s: %w", path, err)
	}
	config.path = absolute

	if err := config.resolve(filepath.Dir(absolute), e); err != nil {
		return nil, err
	}
	return config, nil
}

// Discover returns the configuration file to load. The explicit path wins when
// set. Otherwise the user configuration is preferred over the system
// configuration. A file that exists but cannot be read is returned as an error
// rather than skipped.
func (e Environment) Discover(explicit string) (string, error) {
	if explicit != "" {
		path, err := ExpandPath(explicit, e)
		if err != nil {
			return "", fmt.Errorf("configuration path: %w", err)
		}
		exists, err := regularFile(path)
		if err != nil {
			return "", fmt.Errorf("configuration file %s: %w", path, err)
		}
		if !exists {
			return "", fmt.Errorf("configuration file %s: %w", path, fs.ErrNotExist)
		}
		return path, nil
	}

	user, err := e.userConfig()
	if err != nil {
		return "", err
	}
	exists, err := regularFile(user)
	if err != nil {
		return "", fmt.Errorf("configuration file %s: %w", user, err)
	}
	if exists {
		return user, nil
	}

	system := e.systemConfig()
	exists, err = regularFile(system)
	if err != nil {
		return "", fmt.Errorf("configuration file %s: %w", system, err)
	}
	if exists {
		return system, nil
	}

	return "", fmt.Errorf("no configuration file found (looked for %s and %s)", user, system)
}

func (e Environment) userConfig() (string, error) {
	if dir := e.getenv("XDG_CONFIG_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "cairn", "config.yaml"), nil
	}

	home, err := e.homeDir()
	if err != nil {
		return "", fmt.Errorf("locate user configuration: %w", err)
	}
	if home == "" {
		return "", errors.New("locate user configuration: home directory is not set")
	}
	return filepath.Join(home, ".config", "cairn", "config.yaml"), nil
}

func regularFile(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	// Follow symlinks explicitly so a dangling link is an error, not a
	// missing file that silently falls back to another configuration.
	if info.Mode()&fs.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false, err
		}
	}
	if info.IsDir() {
		return false, errors.New("is a directory")
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("is not a regular file")
	}
	return true, nil
}

func (c *Config) resolve(dir string, env Environment) error {
	for _, name := range sortedKeys(c.Repositories) {
		repository := c.Repositories[name]
		if repository == nil {
			return fmt.Errorf("repository %q: definition is empty", name)
		}
		if repository.Location != "" && ClassifyLocation(repository.Location) == LocationLocal {
			path, err := resolvePath(repository.Location, dir, env)
			if err != nil {
				return fmt.Errorf("repository %q location: %w", name, err)
			}
			repository.Location = path
		}

		path, err := resolveOptionalPath(repository.PassphraseFile, dir, env)
		if err != nil {
			return fmt.Errorf("repository %q passphrase_file: %w", name, err)
		}
		repository.PassphraseFile = path

		path, err = resolveOptionalPath(repository.IdentityFile, dir, env)
		if err != nil {
			return fmt.Errorf("repository %q identity_file: %w", name, err)
		}
		repository.IdentityFile = path
	}

	for _, name := range sortedKeys(c.Jobs) {
		job := c.Jobs[name]
		if job == nil {
			return fmt.Errorf("job %q: definition is empty", name)
		}
		if job.SourceRoot != "" {
			path, err := resolvePath(job.SourceRoot, dir, env)
			if err != nil {
				return fmt.Errorf("job %q source_root: %w", name, err)
			}
			job.SourceRoot = path
		}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveOptionalPath(value, dir string, env Environment) (string, error) {
	if value == "" {
		return "", nil
	}
	return resolvePath(value, dir, env)
}

func resolvePath(value, dir string, env Environment) (string, error) {
	expanded, err := ExpandPath(value, env)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded), nil
	}
	return filepath.Join(dir, expanded), nil
}

func isNameStart(char byte) bool {
	return char == '_' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isNameChar(char byte) bool {
	return isNameStart(char) || (char >= '0' && char <= '9')
}

func validName(name string) bool {
	if name == "" || !isNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isNameChar(name[i]) {
			return false
		}
	}
	return true
}
