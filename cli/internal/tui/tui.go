// Package tui provides Cairn's terminal interface.
package tui

import (
	"context"
	"io"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/Besi97/cairn-backup/cli/internal/workflow"
)

type page int

const (
	homePage page = iota
	workflowsPage
	configurationPage
	archivesPage
	historyPage
	helpPage
	workflowPage
	repositoryPage
	yamlPage
	archiveListPage
	fileListPage
	filePage
	runPage
	taskPage
	archiveDetailPage
	confirmPage
)

// Model owns navigation and a validated TUI snapshot, separate from any agent.
type Model struct {
	selected       *config.Config
	page           page
	width          int
	height         int
	keys           keyMap
	body           viewport.Model
	details        viewport.Model
	detailKey      string
	title          string
	subtitle       string
	notice         string
	workflow       int
	task           int
	repositoryFrom page
	helpFrom       page
	yamlFrom       page
	yamlFields     []string
	reloading      bool
	status         string
	browser        *archiveBrowser
	history        *historyBrowser
	runs           *manualRunner
	prepared       *workflow.Manual
	confirmFrom    page
	exiting        bool
	interrupted    bool
}

// New opens Home with an already validated configuration.
func New(selected *config.Config) Model {
	body := viewport.New()
	body.SoftWrap = true
	details := viewport.New()
	details.SoftWrap = true
	return Model{selected: selected, keys: newKeyMap(), body: body, details: details,
		browser: newArchiveBrowser(), history: newHistoryBrowser(), runs: newManualRunner()}
}

// Run uses only the supplied streams and leaves signal ownership with the CLI.
func Run(ctx context.Context, input io.Reader, output io.Writer, selected *config.Config) error {
	model := New(selected)
	model.browser.ctx = ctx
	model.history.ctx = ctx
	model.runs.ctx = ctx
	defer model.browser.close()
	defer model.history.close()
	defer model.runs.close()
	_, err := tea.NewProgram(model, tea.WithContext(ctx),
		tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler()).Run()
	return err
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case manualProgressWait:
		m.runs.message = msg.message
		m.refresh()
		return m, msg.next
	case manualDone:
		m.runs.active = false
		m.runs.message = msg.message
		if m.exiting {
			if m.interrupted {
				return m, tea.Interrupt
			}
			return m, tea.Quit
		}
		return m.refreshHistoryAfterRun()
	case historyRefresh:
		if msg.job != m.history.job {
			return m, nil
		}
		return m.startHistory()
	case scanUpdate:
		return m.updateScan(msg)
	case historyUpdate:
		return m.updateHistory(msg)
	case tea.PasteMsg:
		if m.browser.editing {
			return m.editSearch(tea.KeyPressMsg{Text: msg.Content})
		}
	case tea.WindowSizeMsg:
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		m.refresh()
		return m, nil
	case tea.KeyPressMsg:
		if m.exiting {
			return m, nil
		}
		if m.page == confirmPage {
			switch {
			case key.Matches(msg, m.keys.Interrupt), key.Matches(msg, m.keys.Quit):
				m.prepared = nil
				m.browser.stop()
				m.history.stop()
				if key.Matches(msg, m.keys.Interrupt) {
					return m, tea.Interrupt
				}
				return m, tea.Quit
			case key.Matches(msg, m.keys.Back):
				m.prepared, m.page = nil, m.confirmFrom
				return m.changed()
			case msg.Code == 'y':
				cmd := m.runs.start(*m.prepared)
				m.prepared, m.page = nil, m.confirmFrom
				m.refresh()
				return m, cmd
			}
			var cmd tea.Cmd
			m.body, cmd = m.body.Update(msg)
			return m, cmd
		}
		if m.browser.editing && !key.Matches(msg, m.keys.Interrupt) {
			return m.editSearch(msg)
		}
		if !m.reloading && !key.Matches(msg, m.keys.Reload) {
			m.status = ""
		}
		if m.split() && key.Matches(msg, m.keys.Page) {
			var cmd tea.Cmd
			m.details, cmd = m.details.Update(msg)
			return m, cmd
		}
		switch {
		case key.Matches(msg, m.keys.Interrupt):
			m.browser.stop()
			m.history.stop()
			if m.runs.active {
				m.exiting, m.interrupted = true, true
				m.runs.cancel()
				m.refresh()
				return m, nil
			}
			return m, tea.Interrupt
		case key.Matches(msg, m.keys.Quit):
			m.browser.stop()
			m.history.stop()
			if m.runs.active {
				m.exiting = true
				m.runs.cancel()
				m.refresh()
				return m, nil
			}
			return m, tea.Quit
		case (m.page == workflowsPage || m.page == workflowPage) && msg.Code == 's':
			if m.runs.active || m.reloading || m.workflowName() == "" {
				return m, nil
			}
			manual, err := workflow.PrepareManual(m.selected, m.workflowName(), time.Now())
			if err != nil {
				m.status = "Cannot prepare workflow. Check the configuration and local lock state."
				m.refresh()
				return m, nil
			}
			m.prepared, m.confirmFrom, m.page = &manual, m.page, confirmPage
			return m.changed()
		case (m.page == historyPage || m.page == runPage) && key.Matches(msg, m.keys.Refresh):
			return m.startHistory()
		case key.Matches(msg, m.keys.Reload):
			if m.reloading {
				return m, nil
			}
			m.reloading, m.status = true, "Reloading TUI configuration..."
			m.refresh()
			return m, reload(m.selected.Path)
		case key.Matches(msg, m.keys.Help):
			if m.page == helpPage {
				m.page = m.helpFrom
			} else {
				m.helpFrom, m.page = m.page, helpPage
			}
			return m.changed()
		case key.Matches(msg, m.keys.Next, m.keys.Previous):
			m.browser.stop()
			if m.page == historyPage || m.page == runPage {
				m.history.stop()
			}
			m.browser.rows = nil
			m.browser.archive = archive.Archive{}
			direction := 1
			if key.Matches(msg, m.keys.Previous) {
				direction = -1
			}
			m.page = (m.tab() + page(direction) + 6) % 6
			if m.page == historyPage && !m.history.attempted {
				m.body.GotoTop()
				return m.startHistory()
			}
			return m.changed()
		case key.Matches(msg, m.keys.Back):
			switch m.page {
			case historyPage:
				m.history.stop()
				m.page = homePage
			case runPage:
				m.page = historyPage
			case archiveListPage:
				m.browser.stop()
				m.browser.rows = nil
				m.page = m.browser.from
			case fileListPage:
				m.page = archiveListPage
				m.browser.archive, m.browser.query = archive.Archive{}, ""
				return m.startBrowse(false)
			case filePage:
				m.page = fileListPage
			case archiveDetailPage:
				m.page = archiveListPage
			case workflowPage:
				m.page = workflowsPage
			case repositoryPage:
				m.page = m.repositoryFrom
			case taskPage:
				m.page = workflowPage
			case yamlPage:
				m.page = m.yamlFrom
			case helpPage:
				m.page = m.helpFrom
			default:
				m.page = homePage
			}
			return m.changed()
		case key.Matches(msg, m.keys.Open):
			switch {
			case m.page == historyPage && len(m.history.runs) > 0:
				m.page = runPage
			case m.page == archivesPage && len(m.repositoryNames()) > 0:
				m.browser.from = archivesPage
				m.browser.name = m.repositoryNames()[m.browser.repository]
				m.browser.archive, m.browser.query = archive.Archive{}, ""
				if !m.split() {
					m.repositoryFrom = archivesPage
					m.page = repositoryPage
					return m.changed()
				}
				m.page = archiveListPage
				return m.startBrowse(false)
			case m.page == repositoryPage:
				m.browser.from = repositoryPage
				m.browser.name = m.repositoryName()
				m.browser.archive, m.browser.query = archive.Archive{}, ""
				m.page = archiveListPage
				return m.startBrowse(false)
			case m.page == archiveListPage && len(m.browser.rows) > 0 && !m.split():
				m.page = archiveDetailPage
			case (m.page == archiveListPage || m.page == archiveDetailPage) && len(m.browser.rows) > 0:
				m.browser.archive = m.browser.rows[m.browser.cursor].archive
				if m.browser.archive.Name == "" {
					return m, nil
				}
				m.browser.query = ""
				m.page = fileListPage
				return m.startBrowse(true)
			case m.page == fileListPage && len(m.browser.rows) > 0:
				m.page = filePage
			case m.page == workflowsPage && len(m.workflowNames()) > 0:
				m.page, m.task = workflowPage, 0
			case m.page == workflowPage && len(m.tasks()) > 0:
				m.page = taskPage
			case m.page == taskPage && len(m.tasks()) > 0:
				m.repositoryFrom = m.page
				m.page = repositoryPage
			default:
				return m, nil
			}
			return m.changed()
		case m.page == historyPage && key.Matches(msg, m.keys.Scroll):
			direction := 1
			if msg.Code == tea.KeyUp || msg.Code == 'k' {
				direction = -1
			}
			m.history.cursor = max(0, min(m.history.cursor+direction, len(m.history.runs)-1))
			return m.changed()
		case m.page == fileListPage && msg.Code == '/':
			m.browser.editing, m.browser.input = true, m.browser.query
			m.refresh()
			return m, nil
		case m.browserPage() && key.Matches(msg, m.keys.Scroll):
			direction := 1
			if msg.Code == tea.KeyUp || msg.Code == 'k' {
				direction = -1
			}
			switch m.page {
			case archivesPage:
				m.browser.repository = max(0, min(m.browser.repository+direction, len(m.repositoryNames())-1))
			case archiveListPage, fileListPage:
				m.browser.cursor = max(0, min(m.browser.cursor+direction, len(m.browser.rows)-1))
			}
			if m.page != filePage {
				return m.changed()
			}
		case key.Matches(msg, m.keys.YAML):
			switch m.page {
			case workflowsPage, workflowPage, taskPage:
				if m.workflowName() == "" {
					return m, nil
				}
				m.yamlFields = []string{"workflows", m.workflowName()}
			case repositoryPage:
				m.yamlFields = []string{"repositories", m.repositoryName()}
			case archivesPage:
				names := m.repositoryNames()
				if len(names) == 0 {
					return m, nil
				}
				m.yamlFields = []string{"repositories", names[m.browser.repository]}
			case configurationPage:
				m.yamlFields = nil
			default:
				return m, nil
			}
			m.yamlFrom, m.page = m.page, yamlPage
			return m.changed()
		case (m.page == workflowsPage || m.page == workflowPage) && key.Matches(msg, m.keys.Scroll):
			direction := 1
			if msg.Code == tea.KeyUp || msg.Code == 'k' {
				direction = -1
			}
			if m.page == workflowsPage {
				m.workflow = max(0, min(m.workflow+direction, len(m.workflowNames())-1))
			} else {
				m.task = max(0, min(m.task+direction, len(m.tasks())-1))
			}
			m.refresh()
			return m, nil
		}
	case reloadMsg:
		m.reloading = false
		m.body.GotoTop()
		if msg.selected == nil {
			m.status = "Reload failed: cannot read or validate selected configuration. Previous TUI snapshot retained."
		} else {
			m.browser.stop()
			m.browser.rows = nil
			m.browser.archive = archive.Archive{}
			m.browser.repository = 0
			name := m.workflowName()
			m.selected = msg.selected
			m.workflow = max(0, slices.Index(m.workflowNames(), name))
			m.task = 0
			m.page = m.tab()
			m.helpFrom, m.yamlFrom, m.yamlFields = homePage, homePage, nil
			m.status = "TUI configuration reloaded. Agent snapshot unchanged."
			m.body.GotoTop()
		}
		m.refresh()
		return m, nil
	}
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return m, cmd
}

func (m Model) changed() (tea.Model, tea.Cmd) {
	m.body.GotoTop()
	m.refresh()
	return m, nil
}

func (m Model) selectablePage() bool {
	return m.page == workflowsPage || m.page == workflowPage ||
		m.page == archivesPage || m.page == archiveListPage || m.page == fileListPage || m.page == historyPage
}

func (m Model) View() tea.View {
	return m.inspectorView()
}
