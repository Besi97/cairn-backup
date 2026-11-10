package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Support IANA zones in minimal installations too.

	"github.com/robfig/cron/v3"
)

var (
	identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	lockWait   = regexp.MustCompile(`^(?:[1-9][0-9]?|[1-5][0-9]{2}|600)s$|^(?:[1-9]|10)m$`)
	keepWithin = regexp.MustCompile(`^[1-9][0-9]*[Hdwmy]$`)
)

// ParseSchedule is the shared six-field cron parser for validation and scheduling.
// Descriptors and embedded timezone directives are not supported; timezone is a
// separate configuration field. Errors never include the expression.
func ParseSchedule(expression string) (cron.Schedule, error) {
	fields := strings.Fields(expression)
	if len(fields) != 6 || strings.HasPrefix(fields[0], "TZ=") || strings.HasPrefix(fields[0], "CRON_TZ=") {
		return nil, errors.New("expected six cron fields")
	}
	parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(expression)
	if err != nil {
		return nil, errors.New("invalid six-field cron expression")
	}
	return schedule, nil
}

// Validate checks static configuration semantics without accessing configured
// files, secrets, Borg, or repositories. It may load system or bundled timezone
// data. Independent errors are returned in stable order.
// Validate before resolution so path normalization cannot hide invalid input.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("configuration is nil")
	}
	var problems []error
	add := func(field, reason string) { problems = append(problems, fmt.Errorf("%s: %s", field, reason)) }
	if c.Version != 1 {
		add("version", "must be 1")
	}
	if len(c.Repositories) == 0 {
		add("repositories", "at least one repository is required")
	}
	if len(c.Jobs) == 0 {
		add("jobs", "at least one job is required")
	}
	for _, name := range sortedKeys(c.Repositories) {
		context := entryContext("repository", name)
		if !identifier.MatchString(name) {
			add(context, "name must start with a lowercase letter and contain only lowercase letters, digits, or hyphens (maximum 63 characters)")
		}
		r := c.Repositories[name]
		if r == nil {
			add(context, "definition is required")
			continue
		}
		if r.Location == "" {
			add(context+" location", "is required")
		}
		if r.IdentityFile != "" && ClassifyLocation(r.Location) != LocationSSH {
			add(context+" identity_file", "is supported only for SSH repositories")
		}
		if !lockWait.MatchString(r.LockWait) {
			add(context+" lock_wait", "must use whole seconds or minutes from 1s through 600s or 1m through 10m")
		}
	}
	prefixes := make(map[string]map[string]bool)
	for _, name := range sortedKeys(c.Jobs) {
		context := entryContext("job", name)
		if !identifier.MatchString(name) {
			add(context, "name must start with a lowercase letter and contain only lowercase letters, digits, or hyphens (maximum 63 characters)")
		}
		j := c.Jobs[name]
		if j == nil {
			add(context, "definition is required")
			continue
		}
		if !identifier.MatchString(j.Repository) {
			add(context+" repository", "must be a valid repository identifier")
		} else if _, ok := c.Repositories[j.Repository]; !ok {
			add(context+" repository", "must reference a configured repository")
		}
		if j.SourceRoot == "" {
			add(context+" source_root", "is required")
		}
		if len(j.Sources) == 0 {
			add(context+" sources", "at least one source is required")
		}
		seen := make(map[string]bool)
		for i, source := range j.Sources {
			field := fmt.Sprintf("%s sources[%d]", context, i)
			if source == "" || path.IsAbs(source) || hasParentSegment(source) {
				add(field, "must be a nonempty relative path without parent (..) segments")
			}
			if seen[source] {
				add(field, "duplicate source")
			}
			seen[source] = true
		}
		if !identifier.MatchString(j.ArchivePrefix) {
			add(context+" archive_prefix", "must be a lowercase identifier of at most 63 characters")
		} else {
			if prefixes[j.Repository] == nil {
				prefixes[j.Repository] = make(map[string]bool)
			}
			if prefixes[j.Repository][j.ArchivePrefix] {
				add(context+" archive_prefix", "must be unique among jobs using the same repository")
			}
			prefixes[j.Repository][j.ArchivePrefix] = true
		}
		if s := j.Schedule; s != nil {
			if _, err := ParseSchedule(s.Cron); err != nil {
				add(context+" schedule.cron", "must be a valid six-field cron expression (second minute hour day-of-month month day-of-week)")
			}
			if s.timezoneSet || s.Timezone != "" {
				// time.LoadLocation accepts empty and Local as special cases,
				// neither of which is an explicit IANA zone name.
				if s.Timezone == "" || s.Timezone == "Local" {
					add(context+" schedule.timezone", "must be an explicit IANA timezone name")
				} else if _, err := time.LoadLocation(s.Timezone); err != nil {
					add(context+" schedule.timezone", "must be a recognized IANA timezone name")
				}
			}
		}
		if m := j.Maintenance; m != nil {
			if m.Retention == nil && !m.Compact {
				add(context+" maintenance", "requires retention or compact: true")
			}
			if m.AfterBackupWarning != "skip" && m.AfterBackupWarning != "run" && (m.warningSet || m.AfterBackupWarning != "") {
				add(context+" maintenance.after_backup_warning", "must be skip or run")
			}
			if r := m.Retention; r != nil {
				field := context + " maintenance.retention"
				if r.KeepWithin == "" && r.KeepHourly == nil && r.KeepDaily == nil && r.KeepWeekly == nil && r.KeepMonthly == nil && r.KeepYearly == nil {
					add(field, "at least one retention rule is required")
				}
				if (r.withinSet || r.KeepWithin != "") && !keepWithin.MatchString(r.KeepWithin) {
					add(field+".keep_within", "must be a positive integer followed by H, d, w, m, or y")
				}
				for _, rule := range []struct {
					name  string
					value *int
				}{
					{"keep_hourly", r.KeepHourly}, {"keep_daily", r.KeepDaily}, {"keep_weekly", r.KeepWeekly}, {"keep_monthly", r.KeepMonthly}, {"keep_yearly", r.KeepYearly},
				} {
					if rule.value != nil && *rule.value < 1 {
						add(field+"."+rule.name, "must be a positive integer")
					}
				}
			}
		}
	}
	return errors.Join(problems...)
}

func hasParentSegment(source string) bool {
	for _, segment := range strings.Split(source, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// Invalid names may be arbitrary sensitive input. Only schema-valid identifiers
// are safe to include as diagnostic context.
func entryContext(kind, name string) string {
	if identifier.MatchString(name) {
		return fmt.Sprintf("%s %q", kind, name)
	}
	return kind + " [invalid name]"
}
