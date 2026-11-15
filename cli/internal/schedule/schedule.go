// Package schedule defines Cairn's cron dialect without starting a scheduler.
package schedule

import (
	"errors"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Named zones must work without a system zoneinfo installation.

	"github.com/robfig/cron/v3"
)

var (
	parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	// Restrict the library's more permissive syntax (for example, ? and empty
	// list entries) to ordinary wildcards, values, ranges, lists, and steps.
	item = regexp.MustCompile(`^(?:\*|(?:[0-9]+|[A-Za-z]{3})(?:-(?:[0-9]+|[A-Za-z]{3}))?)(?:/[0-9]+)?$`)
)

// Schedule is a parsed, minute-resolution schedule in a fixed location.
type Schedule struct {
	spec     cron.Schedule
	location *time.Location
}

// LoadLocation resolves an IANA timezone or UTC. Empty uses the process's local
// timezone; the literal "Local" is not an explicit timezone name.
// Errors deliberately omit the input and underlying loader diagnostic.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	if name != "Local" {
		if location, err := time.LoadLocation(name); err == nil {
			return location, nil
		}
	}
	return nil, errors.New("invalid timezone; use an IANA name or UTC")
}

// Parse accepts five-field cron and the documented calendar aliases. A nil
// location uses the process's local timezone, not the timezone of a Next caller.
// Parsing checks syntax/ranges, not whether a calendar combination ever occurs.
func Parse(expression string, location *time.Location) (*Schedule, error) {
	expression = strings.TrimSpace(expression)
	switch expression {
	case "@hourly":
		expression = "0 * * * *"
	case "@daily", "@midnight":
		expression = "0 0 * * *"
	case "@weekly":
		expression = "0 0 * * 0"
	case "@monthly":
		expression = "0 0 1 * *"
	case "@yearly", "@annually":
		expression = "0 0 1 1 *"
	}
	fields := strings.Fields(expression)
	if len(fields) != 5 {
		return nil, errors.New("invalid cron; use five fields or a supported calendar alias")
	}
	for _, field := range fields {
		for _, part := range strings.Split(field, ",") {
			if !item.MatchString(part) {
				return nil, errors.New("invalid cron field; use values, wildcards, lists, ranges, or steps")
			}
		}
	}
	// Parsing normalized fields also prevents the library from interpreting
	// timezone directives, whose syntax is outside Cairn's contract.
	spec, err := parser.Parse(strings.Join(fields, " "))
	if err != nil {
		return nil, errors.New("invalid cron field or range")
	}
	if location == nil {
		location = time.Local
	}
	return &Schedule{spec: spec, location: location}, nil
}

// Next returns the first occurrence strictly after after, preserving the
// caller's output timezone. It returns zero if the parser finds no occurrence
// within its five-year search horizon. Replay/catch-up policy belongs to the agent.
func (s *Schedule) Next(after time.Time) time.Time {
	next := s.spec.Next(after.In(s.location))
	if next.IsZero() {
		return next
	}
	return next.In(after.Location())
}
