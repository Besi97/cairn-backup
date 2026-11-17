package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/history"
)

type historyUpdate struct {
	job  *historyJob
	runs []history.Observation
	at   time.Time
	err  error
}

type historyJob struct {
	cancel context.CancelFunc
	done   chan struct{}
	update chan historyUpdate
}

type historyBrowser struct {
	ctx       context.Context
	source    func(context.Context) ([]history.Observation, error)
	job       *historyJob
	runs      []history.Observation
	cursor    int
	attempted bool
	loading   bool
	at        time.Time
	status    string
}

func newHistoryBrowser() *historyBrowser {
	return &historyBrowser{ctx: context.Background(), source: readHistory}
}

func readHistory(ctx context.Context) ([]history.Observation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := history.OpenReadOnly()
	if err != nil {
		return nil, history.ErrUnavailable
	}
	runs, err := store.Inspect()
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		return nil, history.ErrUnavailable
	}
	return runs, ctx.Err()
}

func (b *historyBrowser) stop() {
	if b.job != nil {
		b.job.cancel()
	}
	if b.loading {
		b.status = "History read cancelled. g refresh."
	}
	b.loading = false
}

func (b *historyBrowser) close() {
	b.stop()
	if b.job != nil {
		<-b.job.done
	}
}

func (b *historyBrowser) start() tea.Cmd {
	if b.loading {
		return nil
	}
	if b.job != nil {
		select {
		case <-b.job.done:
		default:
			return nil
		}
	}
	ctx, cancel := context.WithCancel(b.ctx)
	job := &historyJob{cancel: cancel, done: make(chan struct{}), update: make(chan historyUpdate, 1)}
	b.job, b.attempted, b.loading, b.status = job, true, true, ""
	source := b.source
	go func() {
		defer close(job.done)
		defer cancel()
		runs, err := source(ctx)
		if ctx.Err() == nil {
			job.update <- historyUpdate{job: job, runs: runs, at: time.Now().UTC(), err: err}
		}
	}()
	return func() tea.Msg {
		select {
		case update := <-job.update:
			return update
		case <-job.done:
			select {
			case update := <-job.update:
				return update
			default:
				return nil
			}
		}
	}
}

func (m Model) startHistory() (tea.Model, tea.Cmd) {
	cmd := m.history.start()
	m.refresh()
	return m, cmd
}

func (m Model) updateHistory(update historyUpdate) (tea.Model, tea.Cmd) {
	b := m.history
	if update.job != b.job || !b.loading {
		return m, nil
	}
	b.loading = false
	if update.err != nil {
		b.status = "Local run history unavailable. g retry."
		if !b.at.IsZero() {
			b.status += " Previous snapshot retained."
		}
	} else {
		id := ""
		if b.cursor < len(b.runs) {
			id = b.runs[b.cursor].Summary.RunID
		}
		b.runs, b.at, b.cursor = update.runs, update.at, 0
		found := false
		for i, run := range b.runs {
			if run.Summary.RunID == id {
				b.cursor, found = i, true
				break
			}
		}
		if m.page == runPage && !found {
			m.page = historyPage
		}
		if m.helpFrom == runPage && !found {
			m.helpFrom = historyPage
		}
	}
	m.refresh()
	if m.page == historyPage {
		m.revealSelection()
	}
	return m, nil
}

func recordedOutcome(s history.Summary) string {
	if s.Incomplete || s.Outcome == "" {
		return "unknown"
	}
	return config.DisplayText(string(s.Outcome))
}

func recordedTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "unknown"
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func (m Model) historyText() string {
	b := m.history
	var text strings.Builder
	text.WriteString("History · local recorded runs\n")
	if b.loading {
		text.WriteString("Reading history…\n")
	}
	if b.status != "" {
		text.WriteString(b.status + "\n")
	}
	if !b.at.IsZero() {
		fmt.Fprintf(&text, "Snapshot inspected: %s · g refresh\n", recordedTime(&b.at))
	}
	if m.page == historyPage {
		fmt.Fprintf(&text, "%d retained (limit %d) · newest last event first\n"+
			"↑/↓ select · enter summary · g refresh · esc home\n"+
			"Incomplete ≠ active; partial counts may be incomplete.\n\n", len(b.runs), history.MaxRuns)
		if len(b.runs) == 0 && !b.loading && b.status == "" {
			text.WriteString("No recorded runs.")
		}
		for i, run := range b.runs {
			s, marker := run.Summary, "  "
			if i == b.cursor {
				marker = "> "
			}
			name := s.Workflow
			if name == "" {
				name = s.Operation
			}
			flags := ""
			if s.Incomplete {
				flags += " · incomplete"
			}
			if s.Partial {
				flags += " · partial"
			}
			if s.Preview {
				flags += " · preview"
			}
			fmt.Fprintf(&text, "%s%s  %s  %s%s\n    %s\n", marker,
				recordedTime(&s.LastObserved), config.DisplayText(name), recordedOutcome(s), flags,
				config.DisplayText(s.RunID))
		}
		return text.String()
	}
	if b.cursor >= len(b.runs) {
		return text.String()
	}
	run := b.runs[b.cursor]
	s := run.Summary
	configID := s.ConfigID
	if configID == "" {
		configID = "unknown"
	}
	owner, lock := string(run.Owner), string(run.Lock)
	if owner == "" {
		owner = "unknown"
	}
	if lock == "" {
		lock = "unknown"
	}
	fmt.Fprintf(&text, "Run: %s · esc history\n\n"+
		"Workflow: %s\nOperation: %s\nSource: %s · preview: %t\n"+
		"Recorded outcome: %s · reason: %s\nIncomplete: %t · partial: %t\n"+
		"First event: %s\nLast event: %s\nStarted: %s\nFinished: %s\nDue: %s\nLast sequence: %d\n\n"+
		"Recorded terminal task counts\nSuccess: %d · warning: %d · failure: %d\nInterrupted: %d · skipped: %d\n"+
		"Per-stage/task details are not retained; partial counts may be incomplete.\n\n"+
		"Momentary evidence · owner: %s · lock: %s\n"+
		"Owner present is not Borg liveness; gone does not mean Borg exited.\n"+
		"Lock held may belong to another run; unknown does not mean free.\n"+
		"Incomplete means no terminal event, not active. Age is not liveness.\n\n"+
		"Configuration identity: %s\n"+
		"Path identity only, not a content hash. Configuration contents are not retained;\n"+
		"differences from TUI/agent snapshots are unknown.",
		config.DisplayText(s.RunID), config.DisplayText(s.Workflow), config.DisplayText(s.Operation),
		config.DisplayText(string(s.Source)), s.Preview, recordedOutcome(s), config.DisplayText(string(s.Reason)),
		s.Incomplete, s.Partial, recordedTime(&s.FirstObserved), recordedTime(&s.LastObserved),
		recordedTime(s.StartedAt), recordedTime(s.FinishedAt), recordedTime(&s.ScheduledAt), s.Sequence,
		s.Tasks.Success, s.Tasks.Warning, s.Tasks.Failure, s.Tasks.Interrupted, s.Tasks.Skipped,
		config.DisplayText(owner), config.DisplayText(lock), config.DisplayText(configID))
	return text.String()
}
