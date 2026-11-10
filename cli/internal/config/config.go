// Package config loads and resolves Cairn's YAML configuration for both the
// command line and the scheduler agent.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Config is a loaded and resolved configuration. Local file paths are
// absolute; SSH repository locations are left verbatim.
type Config struct {
	Version      int                    `yaml:"version"`
	Repositories map[string]*Repository `yaml:"repositories"`
	Jobs         map[string]*Job        `yaml:"jobs"`

	path string
}

// Path returns the absolute path of the file this configuration was loaded
// from.
func (c *Config) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Dir returns the directory that relative paths in the configuration resolve
// from.
func (c *Config) Dir() string {
	if c == nil || c.path == "" {
		return ""
	}
	return filepath.Dir(c.path)
}

// Repository is a configured Borg repository.
type Repository struct {
	Location       string `yaml:"location"`
	PassphraseFile string `yaml:"passphrase_file"`
	IdentityFile   string `yaml:"identity_file"`
	LockWait       string `yaml:"lock_wait"`
}

// ReadPassphrase returns the passphrase for the named repository, or an empty
// string when that repository has no passphrase file. The file is read when
// this is called, not when the configuration is loaded. Errors identify the
// repository and field but never include the secret.
func (c *Config) ReadPassphrase(name string) (string, error) {
	if c == nil {
		return "", errors.New("configuration is nil")
	}
	repository, ok := c.Repositories[name]
	if !ok {
		return "", fmt.Errorf("repository %q is not configured", name)
	}
	if repository == nil || repository.PassphraseFile == "" {
		return "", nil
	}
	secret, err := ReadSecret(repository.PassphraseFile)
	if err != nil {
		return "", fmt.Errorf("repository %q passphrase_file: %w", name, err)
	}
	return secret, nil
}

// Job is a configured backup job.
type Job struct {
	Repository    string       `yaml:"repository"`
	SourceRoot    string       `yaml:"source_root"`
	Sources       []string     `yaml:"sources"`
	ArchivePrefix string       `yaml:"archive_prefix"`
	Schedule      *Schedule    `yaml:"schedule"`
	Maintenance   *Maintenance `yaml:"maintenance"`
}

// SourcePaths returns the configured sources joined to the resolved source
// root. The raw Sources stay relative for Borg.
func (j *Job) SourcePaths() []string {
	if j == nil {
		return nil
	}
	paths := make([]string, len(j.Sources))
	for i, source := range j.Sources {
		paths[i] = filepath.Join(j.SourceRoot, source)
	}
	return paths
}

// Schedule controls when the agent runs a job.
type Schedule struct {
	Cron        string `yaml:"cron"`
	Timezone    string `yaml:"timezone"`
	timezoneSet bool
}

// Maintenance configures post-backup retention and compaction.
type Maintenance struct {
	Retention          *Retention `yaml:"retention"`
	Compact            bool       `yaml:"compact"`
	AfterBackupWarning string     `yaml:"after_backup_warning"`
	warningSet         bool
}

// Retention holds Borg prune rules.
type Retention struct {
	KeepWithin  string `yaml:"keep_within"`
	KeepHourly  *int   `yaml:"keep_hourly"`
	KeepDaily   *int   `yaml:"keep_daily"`
	KeepWeekly  *int   `yaml:"keep_weekly"`
	KeepMonthly *int   `yaml:"keep_monthly"`
	KeepYearly  *int   `yaml:"keep_yearly"`
	withinSet   bool
}
