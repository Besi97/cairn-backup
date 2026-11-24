// Package metrics collects bounded workflow observations without a transport.
package metrics

import (
	"errors"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Besi97/cairn-backup/cli/internal/history"
	"github.com/Besi97/cairn-backup/cli/internal/lifecycle"
)

const MaxWorkflows = 256
const MaxActive = 1024

var ErrWorkflows = errors.New("invalid metrics workflow names")

type Workflow struct {
	Name string
	// Counters are events observed by this collector since its creation.
	Success, Warning, Failure, Interrupted, Skipped, Busy uint64
	InFlight                                              int
	// False means the active-run limit was exceeded, so InFlight is a lower bound.
	InFlightKnown bool
	LastOutcome   lifecycle.Outcome
	LastFinished  time.Time
	LastDuration  time.Duration
	DurationKnown bool
	// LastSuccess is the latest known successful workflow completion.
	LastSuccess  time.Time
	SuccessKnown bool
}

type Snapshot struct {
	Workflows        []Workflow
	HistoryAvailable bool
}

type active struct {
	name  string
	start time.Time
}
type Collector struct {
	mu        sync.Mutex
	configID  string
	workflows map[string]*Workflow
	active    map[string]active
}

// New fixes the set of labels for the collector's lifetime. Config identity is
// used only for filtering and is never returned. Names must not contain secrets.
func New(configID string, names []string) (*Collector, error) {
	if configID == "" || len(names) > MaxWorkflows {
		return nil, ErrWorkflows
	}
	c := &Collector{configID: configID, workflows: make(map[string]*Workflow), active: make(map[string]active)}
	for _, name := range names {
		if name == "" || len(name) > history.MaxLabelBytes || !utf8.ValidString(name) {
			return nil, ErrWorkflows
		}
		if _, ok := c.workflows[name]; ok {
			return nil, ErrWorkflows
		}
		c.workflows[name] = &Workflow{Name: name, InFlightKnown: true}
	}
	return c, nil
}

// Observe is suitable for lifecycle.WithAdditionalObserver. It never affects execution.
func (c *Collector) Observe(e lifecycle.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.workflows[e.Workflow]
	if w == nil || e.ConfigID != c.configID || e.Preview {
		return nil
	}
	switch e.Kind {
	case lifecycle.RunStarted:
		if e.RunID == "" {
			w.InFlightKnown = false
			break
		}
		if _, exists := c.active[e.RunID]; exists {
			break
		}
		if len(c.active) == MaxActive {
			w.InFlightKnown = false
			break
		}
		c.active[e.RunID] = active{e.Workflow, e.Time}
		w.InFlight++
	case lifecycle.RunFinished, lifecycle.RunSkipped:
		a, ok := c.active[e.RunID]
		ok = ok && a.name == e.Workflow
		if ok {
			delete(c.active, e.RunID)
			w.InFlight--
		}
		if !e.Time.IsZero() && (w.LastFinished.IsZero() || e.Time.After(w.LastFinished)) {
			w.LastFinished, w.LastOutcome = e.Time, e.Outcome
			w.DurationKnown = ok && !a.start.IsZero() && !e.Time.Before(a.start)
			w.LastDuration = 0
			if w.DurationKnown {
				w.LastDuration = e.Time.Sub(a.start)
			}
		}
		if e.Outcome == lifecycle.Success && !e.Time.IsZero() && (!w.SuccessKnown || e.Time.After(w.LastSuccess)) {
			w.LastSuccess, w.SuccessKnown = e.Time, true
		}
		switch e.Outcome {
		case lifecycle.Success:
			w.Success++
		case lifecycle.Warning:
			w.Warning++
		case lifecycle.Failure:
			w.Failure++
		case lifecycle.Interrupted:
			w.Interrupted++
		case lifecycle.Skipped:
			w.Skipped++
		}
		if e.Reason == lifecycle.Busy {
			w.Busy++
		}
	}
	return nil
}

// Snapshot combines process-local observations with a best-effort history read.
// Open a fresh read-only store for each call so newly created history is visible.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	result := Snapshot{Workflows: make([]Workflow, 0, len(c.workflows))}
	for _, w := range c.workflows {
		result.Workflows = append(result.Workflows, *w)
	}
	c.mu.Unlock()
	sort.Slice(result.Workflows, func(i, j int) bool { return result.Workflows[i].Name < result.Workflows[j].Name })
	store, err := history.OpenReadOnly()
	if err != nil {
		return result
	}
	defer store.Close()
	runs, err := store.List()
	if err != nil {
		return result
	}
	result.HistoryAvailable = true
	byName := make(map[string]*Workflow, len(result.Workflows))
	for i := range result.Workflows {
		byName[result.Workflows[i].Name] = &result.Workflows[i]
	}
	for _, r := range runs {
		w := byName[r.Workflow]
		if w == nil || r.ConfigID != c.configID || r.Preview || r.Incomplete || r.FinishedAt == nil {
			continue
		}
		if w.LastFinished.IsZero() || r.FinishedAt.After(w.LastFinished) {
			w.LastFinished, w.LastOutcome = *r.FinishedAt, r.Outcome
			w.DurationKnown = r.StartedAt != nil && !r.FinishedAt.Before(*r.StartedAt)
			if w.DurationKnown {
				w.LastDuration = r.FinishedAt.Sub(*r.StartedAt)
			} else {
				w.LastDuration = 0
			}
		}
		if r.Outcome == lifecycle.Success && (!w.SuccessKnown || r.FinishedAt.After(w.LastSuccess)) {
			w.LastSuccess, w.SuccessKnown = *r.FinishedAt, true
		}
	}
	return result
}
