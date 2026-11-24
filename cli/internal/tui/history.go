package tui

import (
	"context"
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

type historyRefresh struct{ job *historyJob }

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

func (m Model) refreshHistoryAfterRun() (tea.Model, tea.Cmd) {
	b := m.history
	b.stop()
	b.attempted = false
	if b.job == nil {
		return m.startHistory()
	}
	job := b.job
	m.refresh()
	return m, func() tea.Msg {
		<-job.done
		return historyRefresh{job: job}
	}
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
