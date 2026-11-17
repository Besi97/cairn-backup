package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/charmbracelet/x/ansi"
)

func TestInspectorPreviewAndDetailScrolling(t *testing.T) {
	m, path := browsingModel(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m, _ = press(m, tea.KeyTab, 0)
	if !m.split() {
		t.Fatal("wide workflow list has no inspector")
	}
	if strings.Contains(ansi.Strip(m.body.GetContent()), "Cron:") ||
		!strings.Contains(ansi.Strip(m.details.GetContent()), "Cron: @daily") {
		t.Fatal("workflow list and inspector are not separated")
	}
	m, _ = press(m, tea.KeyEnter, 0)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = resized.(Model)
	list := m.body.View()
	m, _ = press(m, tea.KeyPgDown, 0)
	if m.details.YOffset() == 0 || m.task != 0 || m.body.View() != list {
		t.Fatal("page down did not scroll details independently")
	}
	m, _ = press(m, 'j', 0)
	if m.details.YOffset() != 0 {
		t.Fatal("new selection retained the previous detail scroll position")
	}
	contains(t, m, "Operation: compact", "Repository: remote",
		"Location: ssh://backup@example.invalid/./repo", "Passphrase file: ./not-accessed",
		"Identity file: ./also-not-accessed", "Enter opens task details.")
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != taskPage {
		t.Fatal("opening a task skipped task details")
	}
	contains(t, m, "Enter opens the repository view")
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != workflowPage || m.task != 1 {
		t.Fatal("task details back lost the selected task")
	}
}

func TestNarrowInspectorDrilldown(t *testing.T) {
	m, _ := browsingModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	m, _ = press(m, tea.KeyTab, 0)
	m, _ = press(m, tea.KeyEnter, 0)
	m, _ = press(m, tea.KeyEnter, 0)
	if m.split() || m.page != taskPage {
		t.Fatal("narrow workflow did not open task details")
	}
	contains(t, m, "Sources:", "• .", "Operation: backup")
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != repositoryPage {
		t.Fatal("task did not open repository")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != taskPage {
		t.Fatal("repository did not return to task details")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != workflowPage {
		t.Fatal("task did not return to list")
	}
	next, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = next.(Model)
	if !m.split() || m.task != 0 {
		t.Fatal("expanding terminal lost selection")
	}
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != taskPage {
		t.Fatal("wide inspector skipped task details")
	}
	contains(t, m, "Repository: local", "Location: ./repo", "Passphrase: [redacted]")
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != repositoryPage {
		t.Fatal("task details did not open the repository view")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != taskPage {
		t.Fatal("wide repository view did not return to task details")
	}
}

func TestNarrowArchiveInspectionDoesNotStartListing(t *testing.T) {
	m := archiveModel(t, testSource)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	m, cmd := press(m, tea.KeyEnter, 0)
	if cmd != nil || m.page != repositoryPage || m.tab() != archivesPage {
		t.Fatal("narrow repository inspection started a listing or lost its tab")
	}
	contains(t, m, "Repository: local", "Location: ./repo", "[redacted]")
	m, _ = press(m, 'y', 0)
	if m.tab() != archivesPage {
		t.Fatal("repository YAML lost Archives tab")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	m, cmd = press(m, tea.KeyEnter, 0)
	m = finishScan(t, m, cmd)
	m, cmd = press(m, tea.KeyEnter, 0)
	if cmd != nil || m.page != archiveDetailPage || m.tab() != archivesPage {
		t.Fatal("narrow archive inspection started file browsing")
	}
	contains(t, m, "Started: 2026", "ID: id", "first")
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != archiveListPage {
		t.Fatal("archive detail did not return to retained list")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != repositoryPage {
		t.Fatal("archive list did not return to repository inspector")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != archivesPage {
		t.Fatal("repository did not return to list")
	}
}

func TestInspectorLayoutBounds(t *testing.T) {
	m, _ := browsingModel(t)
	m.browser.rows = []metadata{{}}
	pages := []page{homePage, workflowsPage, workflowPage, taskPage, repositoryPage,
		configurationPage, yamlPage, archivesPage, archiveListPage, archiveDetailPage, fileListPage, filePage,
		historyPage, runPage, helpPage}
	for _, p := range pages {
		for _, width := range []int{0, 1, 24, 40, 80, 99, 100, 120, 200} {
			for _, height := range []int{0, 1, 6, 9, 10, 15, 16, 24, 40} {
				t.Run(fmt.Sprintf("%d/%dx%d", p, width, height), func(t *testing.T) {
					m.page = p
					next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
					m = next.(Model)
					content := m.View().Content
					if lipgloss.Width(content) > width || lipgloss.Height(content) > max(1, height) {
						t.Fatalf("view overflows: %q", content)
					}
					if width >= 24 && height >= 10 && !strings.Contains(ansi.Strip(content), "? help") {
						t.Fatalf("footer was pushed offscreen:\n%s", ansi.Strip(content))
					}
				})
			}
		}
	}
}

func TestInspectorColorAndNoColor(t *testing.T) {
	m, _ := browsingModel(t)
	m, _ = press(m, tea.KeyTab, 0)
	t.Setenv("NO_COLOR", "")
	if !strings.Contains(m.View().Content, "\x1b[") {
		t.Fatal("inspector has no visual styling")
	}
	t.Setenv("NO_COLOR", "1")
	view := m.View().Content
	if strings.Contains(view, "\x1b") || !strings.Contains(view, "> a-nightly") {
		t.Fatal("NO_COLOR lost the text selection indicator or retained ANSI")
	}
}

func TestInspectorSearchKeepsTypingVisible(t *testing.T) {
	m, _ := browsingModel(t)
	m.page = fileListPage
	m.browser.editing = true
	m.browser.input = strings.Repeat("界", 100) + "/needle"
	m.refresh()
	if !strings.Contains(ansi.Strip(m.View().Content), "/needle_") {
		t.Fatalf("long search input hides the insertion point (%d cells, %q):\n%s", ansi.StringWidth(m.notice), m.notice, ansi.Strip(m.View().Content))
	}
}

func TestInspectorConfiguredValuesRemainSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(browseConfig), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	m := New(selected)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = next.(Model)
	m.page, m.browser.repository = archivesPage, 1
	m.refresh()
	contains(t, m, "ssh://backup@example.invalid/./repo", "./not-accessed", "./also-not-accessed")
	m.browser.repository = 0
	m.refresh()
	contains(t, m, "Passphrase: [redacted]", "./repo")
	if strings.Contains(modelText(m), "never-display-this") {
		t.Fatal("inspector leaked a configured credential")
	}
}
