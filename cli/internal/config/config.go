// Package config loads optional configuration without executing Borg or opening
// repository credentials. Only commands needing configuration should call Load.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Besi97/cairn-backup/cli/internal/borg"
)

// Config contains a validated version 1 configuration and its selected location.
// Path and Directory deliberately retain the selected symlink's directory;
// canonical workflow-lock identity is a separate execution concern.
type Config struct {
	Version      int                   `json:"version"`
	Repositories map[string]Repository `json:"repositories"`
	Workflows    map[string]Workflow   `json:"workflows"`
	Path         string                `json:"-"`
	Directory    string                `json:"-"`
}

type Repository struct {
	Location       string  `json:"location"`
	Passphrase     *string `json:"passphrase"`
	PassphraseFile string  `json:"passphrase_file"`
	IdentityFile   string  `json:"identity_file"`
}

type Workflow struct {
	Cron     string  `json:"cron"`
	Timezone string  `json:"timezone"`
	Stages   []Stage `json:"stages"`
}

type Stage struct {
	Tasks []Task `json:"tasks"`
}

// Task is a schema-validated discriminated union. Only fields belonging to Type
// can be populated. Sources, exclusions, and archive templates remain literal.
type Task struct {
	Type          string   `json:"type"`
	Repository    string   `json:"repository"`
	ArchivePrefix string   `json:"archive_prefix"`
	ArchiveName   string   `json:"archive_name"`
	SourceRoot    string   `json:"source_root"`
	Sources       []string `json:"sources"`
	Exclude       []string `json:"exclude"`
	LockWait      *int     `json:"lock_wait"`
	VerifyData    bool     `json:"verify_data"`
	KeepWithin    string   `json:"keep_within"`
	KeepSecondly  int      `json:"keep_secondly"`
	KeepMinutely  int      `json:"keep_minutely"`
	KeepHourly    int      `json:"keep_hourly"`
	KeepDaily     int      `json:"keep_daily"`
	KeepWeekly    int      `json:"keep_weekly"`
	KeepMonthly   int      `json:"keep_monthly"`
	KeepYearly    int      `json:"keep_yearly"`
}

// Options makes discovery independent of process-global state in tests.
// Explicit distinguishes an omitted path from --config "".
type Options struct {
	Path       string
	Explicit   bool
	Directory  string
	Home       string
	XDGHome    string
	SystemPath string
}

// Load selects exactly one file and validates it in full.
func Load(options Options) (*Config, error) {
	if !filepath.IsAbs(options.Directory) {
		return nil, fmt.Errorf("configuration invocation directory must be absolute")
	}
	path, data, err := discover(options)
	if err != nil {
		return nil, err
	}
	doc, value, err := decode(path, data)
	if err != nil {
		return nil, err
	}
	if err := validate(path, doc, value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%s: cannot represent configuration", path)
	}
	var result Config
	if err := json.Unmarshal(encoded, &result); err != nil {
		// Never surface encoding/json errors: they may include secret values.
		return nil, fmt.Errorf("%s: numeric value outside supported integer range", path)
	}
	result.Path, result.Directory = path, filepath.Dir(path)
	if err := result.resolve(options.Home, doc); err != nil {
		return nil, err
	}
	return &result, nil
}

// Environment returns discovery settings for a CLI invocation. Home is resolved
// lazily in effect: its absence matters only when a selected path needs it.
func Environment() (Options, error) {
	dir, err := os.Getwd()
	if err != nil {
		return Options{}, fmt.Errorf("resolve configuration invocation directory: %w", err)
	}
	home, _ := os.UserHomeDir()
	return Options{Directory: dir, Home: home, XDGHome: os.Getenv("XDG_CONFIG_HOME")}, nil
}

func defaultLockWait(task *Task) {
	if task.LockWait == nil {
		value := borg.DefaultLockWait
		task.LockWait = &value
	}
}
