package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/borg"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/charmbracelet/x/ansi"
)

func archiveModel(t *testing.T, source metadataSource) Model {
	t.Helper()
	m, _ := browsingModel(t)
	m.browser.ctx = t.Context()
	m.browser.source = source
	t.Cleanup(m.browser.close)
	m.page = archivesPage
	m.refresh()
	return m
}

func nextScan(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("missing asynchronous command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		next, cmd := m.Update(msg)
		return next.(Model), cmd
	case <-time.After(5 * time.Second):
		t.Fatal("listing blocked")
		return m, nil
	}
}

func finishScan(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for m.browser.loading {
		m, cmd = nextScan(t, m, cmd)
	}
	return m
}

func testSource(_ context.Context, _ *config.Config, _, name string, emit func(metadata) error) (borg.Result, error) {
	if name == "" {
		for _, name := range []string{"first", "second"} {
			if err := emit(metadata{archive: archive.Archive{Name: archive.DisplayText(name), ID: "id", Start: "2026"}}); err != nil {
				return borg.Result{}, err
			}
		}
	} else {
		for i := range 100_000 {
			path := fmt.Sprintf("path/%06d", i)
			if i == 99_999 {
				path = "last/needle"
			}
			if err := emit(metadata{file: archive.File{Path: archive.DisplayText(path), Type: "-", Size: int64(i)}}); err != nil {
				return borg.Result{}, err
			}
		}
	}
	return borg.Result{Outcome: borg.Success}, nil
}

func TestArchiveBrowseAndFullSearch(t *testing.T) {
	m := archiveModel(t, testSource)
	contains(t, m, "> local", "remote")
	m, cmd := press(m, tea.KeyEnter, 0)
	m = finishScan(t, m, cmd)
	contains(t, m, "Complete.", "2 scanned", "> first", "2026")
	m, _ = press(m, 'j', 0)
	m, cmd = press(m, tea.KeyEnter, 0)
	m = finishScan(t, m, cmd)
	contains(t, m, "Files / second", "100000 scanned", "1000 retained", "TRUNCATED", "NOT exact restore selectors")
	if len(m.browser.rows) != maxBrowseRows {
		t.Fatalf("retained %d rows", len(m.browser.rows))
	}
	for range 30 {
		m, _ = press(m, 'j', 0)
	}
	contains(t, m, "> path/000030", "- · 30 B")
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Path (lossy display", "Size: 30 bytes", "UID: 0", "Healthy marker: false")
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, '/', 0)
	for _, r := range "needle" {
		m, _ = press(m, r, 0)
	}
	contains(t, m, "Search> needle_")
	// Editing does not start a scan or filter the retained rows.
	if m.browser.query != "" || len(m.browser.rows) != maxBrowseRows {
		t.Fatal("search applied before Enter")
	}
	m, cmd = press(m, tea.KeyEnter, 0)
	m = finishScan(t, m, cmd)
	contains(t, m, "100000 scanned · 1 matched · 1 retained", "last/needle")
	if m.browser.truncated {
		t.Fatal("search of full archive was truncated")
	}
	m, _ = press(m, '/', 0)
	m, _ = press(m, tea.KeyBackspace, 0)
	m, _ = press(m, 'q', 0)
	if !m.browser.editing || m.browser.input != "needlq" {
		t.Fatal("q quit instead of editing")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.browser.query != "needle" {
		t.Fatal("Escape applied edited query")
	}
}

func TestArchiveScanStatusInPanels(t *testing.T) {
	tests := []struct {
		name, banner, notice string
		outcome              borg.Outcome
		err                  error
		row                  bool
	}{
		{name: "empty", banner: "No retained results.", notice: "Complete.", outcome: borg.Success},
		{name: "failure", banner: "Scan failed", notice: "Failed; results are partial.", outcome: borg.Failure},
		{name: "partial failure", banner: "Scan failed · partial", notice: "Failed; results are partial.",
			outcome: borg.Success, err: errors.New("unsafe-scan-diagnostic"), row: true},
		{name: "warning", banner: "Borg warnings", notice: "Borg warnings; results may be incomplete.", outcome: borg.Warning},
		{name: "cancelled", banner: "Scan cancelled", notice: "Cancelled; results are partial.", outcome: borg.Interrupted},
	}
	for _, files := range []bool{false, true} {
		for _, width := range []int{40, 100} {
			for _, tt := range tests {
				t.Run(fmt.Sprintf("files=%t/width=%d/%s", files, width, tt.name), func(t *testing.T) {
					m := archiveModel(t, func(_ context.Context, _ *config.Config, _, _ string, emit func(metadata) error) (borg.Result, error) {
						if tt.row {
							if err := emit(metadata{archive: archive.Archive{Name: "retained"}, file: archive.File{Path: "retained"}}); err != nil {
								return borg.Result{}, err
							}
						}
						return borg.Result{Outcome: tt.outcome}, tt.err
					})
					m.page, m.browser.name, m.browser.archive.Name = archiveListPage, "local", "test"
					if files {
						m.page = fileListPage
					}
					next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
					m = next.(Model)
					next, cmd := m.startBrowse(files)
					m = next.(Model)
					checkPanel := func(want string) {
						t.Helper()
						list := ansi.Strip(m.body.View())
						if !strings.Contains(list, want) ||
							(want != "No retained results." && strings.Contains(list, "No retained results.")) {
							t.Fatalf("expected %q in list panel, got %q", want, list)
						}
					}
					checkPanel("Scanning…")
					if m.split() && !strings.Contains(ansi.Strip(m.details.View()), "Waiting for results.") {
						t.Fatal("empty inspector did not explain the wait")
					}
					if tt.row {
						m, cmd = nextScan(t, m, cmd)
						checkPanel("Scanning… · partial")
						checkPanel("> retained")
					}
					m = finishScan(t, m, cmd)
					checkPanel(tt.banner)
					if !strings.HasPrefix(m.notice, tt.notice) || strings.Contains(ansi.Strip(m.View().Content), "unsafe-scan-diagnostic") {
						t.Fatalf("unexpected notice or diagnostic in view: %q", m.notice)
					}
					if tt.row {
						checkPanel("> retained")
						if m.split() && !strings.Contains(ansi.Strip(m.details.View()), "Stored metadata") {
							t.Fatal("partial result lost its details")
						}
						next, _ = m.Update(tea.WindowSizeMsg{Width: 24, Height: 10})
						m = next.(Model)
						view := ansi.Strip(m.View().Content)
						if !strings.Contains(view, "Scan failed") || !strings.Contains(view, "> retained") {
							t.Fatalf("smallest layout hid status or selection:\n%s", view)
						}
					}
				})
			}
		}
	}
}

func TestBrowseByteLimitAndOutcomes(t *testing.T) {
	for _, outcome := range []borg.Outcome{borg.Success, borg.Warning, borg.Failure, borg.Interrupted} {
		t.Run(fmt.Sprint(outcome), func(t *testing.T) {
			m := archiveModel(t, func(_ context.Context, _ *config.Config, _, _ string, emit func(metadata) error) (borg.Result, error) {
				for range 20 {
					if err := emit(metadata{file: archive.File{Path: archive.DisplayText(strings.Repeat("x", 1<<20))}}); err != nil {
						return borg.Result{}, err
					}
				}
				var err error
				if outcome == borg.Failure {
					err = errors.New("never-display-this\x1b[31m")
				}
				return borg.Result{Outcome: outcome}, err
			})
			m.page, m.browser.name, m.browser.archive.Name = fileListPage, "local", "test"
			next, cmd := m.startBrowse(true)
			m = finishScan(t, next.(Model), cmd)
			used := 0
			for _, row := range m.browser.rows {
				used += row.bytes()
			}
			if used > maxBrowseBytes || len(m.browser.rows) >= 20 || m.browser.scanned != 20 || !m.browser.truncated {
				t.Fatalf("retained=%d bytes=%d scanned=%d truncated=%v", len(m.browser.rows), used, m.browser.scanned, m.browser.truncated)
			}
			switch outcome {
			case borg.Success:
				contains(t, m, "Complete.")
			case borg.Warning:
				contains(t, m, "Borg warnings", "may be incomplete")
			case borg.Failure:
				contains(t, m, "Failed; results are partial")
			case borg.Interrupted:
				contains(t, m, "Cancelled; results are partial")
			}
			if len(m.body.GetContent()) > 10_000 {
				t.Fatal("rendered oversized list text")
			}
		})
	}
}

func TestScanCancellationReplacementAndStaleMessages(t *testing.T) {
	started, cleaned := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var calls atomic.Int32
	m := archiveModel(t, func(ctx context.Context, _ *config.Config, _, _ string, emit func(metadata) error) (borg.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			<-release
			close(cleaned)
			return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
		}
		select {
		case <-cleaned:
		default:
			t.Error("replacement started before previous cleanup")
		}
		return borg.Result{Outcome: borg.Success}, emit(metadata{file: archive.File{Path: "new"}})
	})
	m.page, m.browser.name, m.browser.archive.Name = fileListPage, "local", "test"
	next, oldCmd := m.startBrowse(true)
	m = next.(Model)
	<-started
	old := m.browser.job
	m, _ = press(m, '/', 0)
	m, _ = press(m, 'n', 0)
	m, cmd := press(m, tea.KeyEnter, 0)
	// UI navigation and resizing do not wait for process cleanup.
	m, _ = press(m, '?', 0)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m = resized.(Model)
	if m.page != helpPage || calls.Load() != 1 {
		t.Fatal("replacement overlapped old scan or blocked navigation")
	}
	close(release)
	m = finishScan(t, m, cmd)
	next, staleCmd := m.Update(scanUpdate{job: old, rows: []metadata{{file: archive.File{Path: "stale"}}}, done: true})
	m = next.(Model)
	if staleCmd != nil || len(m.browser.rows) != 1 || m.browser.rows[0].file.Path != "new" {
		t.Fatal("stale scan replaced new results")
	}
	_ = oldCmd()
	m, _ = press(m, '?', 0)
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != archiveListPage {
		t.Fatal("back did not return to archives")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.browser.loading || len(m.browser.rows) != 0 {
		t.Fatal("leaving listing retained active state")
	}
}

func TestBrowserEscapingEmptyAndSearchBounds(t *testing.T) {
	m := archiveModel(t, func(_ context.Context, _ *config.Config, _, _ string, emit func(metadata) error) (borg.Result, error) {
		return borg.Result{Outcome: borg.Success}, emit(metadata{file: archive.File{
			Path: "a\x1b[31m\nb", Type: "\r", Mode: "\x1b]0;x\a", Mtime: "\u202e",
			LinkTarget: "target\n", UID: 12, GID: 34, Healthy: true,
		}})
	})
	m.page, m.browser.name, m.browser.archive.Name = fileListPage, "local", "test"
	next, cmd := m.startBrowse(true)
	m = finishScan(t, next.(Model), cmd)
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, `a\x1b[31m\nb`, `\x1b]0;x\a`, `\u202e`, `target\n`, "UID: 12", "GID: 34")
	if strings.ContainsAny(ansi.Strip(m.body.GetContent()), "\x1b\r\a\u202e") {
		t.Fatal("metadata injected terminal controls")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, '/', 0)
	next, _ = m.Update(tea.PasteMsg{Content: strings.Repeat("é", maxQueryBytes) + "\x1b\n"})
	m = next.(Model)
	if len(m.browser.input) != maxQueryBytes || strings.ContainsAny(m.browser.input, "\x1b\n") {
		t.Fatal("unbounded or unsafe query")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, tea.KeyTab, 0)
	if m.browser.loading || len(m.browser.rows) != 0 {
		t.Fatal("tab did not release listing")
	}
	m.selected = &config.Config{}
	m.page = archivesPage
	m.refresh()
	m, cmd = press(m, tea.KeyEnter, 0)
	contains(t, m, "No repositories configured.")
	if cmd != nil {
		t.Fatal("empty repository selection started work")
	}
}

func TestArchiveListBoundAndRepositoryBack(t *testing.T) {
	m := archiveModel(t, func(ctx context.Context, _ *config.Config, _, name string, emit func(metadata) error) (borg.Result, error) {
		if name != "" {
			t.Error("archive list unexpectedly inspected files")
		}
		for i := range 100_000 {
			if err := emit(metadata{archive: archive.Archive{Name: archive.DisplayText(fmt.Sprint(i)), ID: "id", Start: "2026"}}); err != nil {
				return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
			}
		}
		return borg.Result{Outcome: borg.Success}, nil
	})
	m.page = repositoryPage
	m, cmd := press(m, tea.KeyEnter, 0)
	m = finishScan(t, m, cmd)
	contains(t, m, "100000 scanned · 100000 matched · 1000 retained", "TRUNCATED")
	if len(m.browser.rows) != maxBrowseRows {
		t.Fatal("archive listing was not bounded")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != repositoryPage || len(m.browser.rows) != 0 {
		t.Fatal("back did not return to configured repository")
	}
}

func TestReloadCancelsListingAndRejectsOldRows(t *testing.T) {
	started := make(chan struct{})
	m := archiveModel(t, func(ctx context.Context, _ *config.Config, _, _ string, emit func(metadata) error) (borg.Result, error) {
		close(started)
		<-ctx.Done()
		return borg.Result{Outcome: borg.Interrupted}, ctx.Err()
	})
	m, cmd := press(m, tea.KeyEnter, 0)
	<-started
	old := m.browser.job
	next, _ := m.Update(reloadMsg{selected: &config.Config{}})
	m = next.(Model)
	if m.page != archivesPage || m.browser.loading {
		t.Fatal("reload kept active listing or lost Archives tab")
	}
	select {
	case <-old.done:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not cancel listing")
	}
	next, _ = m.Update(scanUpdate{job: old, rows: []metadata{{archive: archive.Archive{Name: "old"}}}})
	m = next.(Model)
	if len(m.browser.rows) != 0 {
		t.Fatal("reload accepted stale rows")
	}
	_ = cmd()
	contains(t, m, "No repositories configured.")
}
