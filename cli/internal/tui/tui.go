// Package tui provides Cairn's read-only terminal interface.
package tui

import (
	"context"
	"io"
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Besi97/cairn-backup/cli/internal/archive"
	"github.com/Besi97/cairn-backup/cli/internal/config"
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
)

// Model owns navigation and a validated TUI snapshot, separate from any agent.
// It only reads configuration, archive metadata and local history.
type Model struct {
	selected   *config.Config
	page       page
	width      int
	height     int
	keys       keyMap
	help       help.Model
	body       viewport.Model
	workflow   int
	task       int
	helpFrom   page
	yamlFrom   page
	yamlFields []string
	reloading  bool
	status     string
	browser    *archiveBrowser
	history    *historyBrowser
}

// New opens Home with an already validated configuration.
func New(selected *config.Config) Model {
	h := help.New()
	h.Styles.ShortKey = lipgloss.NewStyle().Bold(true)
	h.Styles.ShortDesc = lipgloss.NewStyle()
	h.Styles.FullKey = h.Styles.ShortKey
	h.Styles.FullDesc = h.Styles.ShortDesc
	body := viewport.New()
	body.SoftWrap = true
	return Model{selected: selected, keys: newKeyMap(), help: h, body: body,
		browser: newArchiveBrowser(), history: newHistoryBrowser()}
}

// Run uses only the supplied streams and leaves signal ownership with the CLI.
func Run(ctx context.Context, input io.Reader, output io.Writer, selected *config.Config) error {
	model := New(selected)
	model.browser.ctx = ctx
	model.history.ctx = ctx
	defer model.browser.close()
	defer model.history.close()
	_, err := tea.NewProgram(model, tea.WithContext(ctx),
		tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler()).Run()
	return err
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
		m.help.SetWidth(m.width)
		m.body.SetWidth(m.width)
		m.body.SetHeight(max(0, m.height-4))
		m.refresh()
		if m.selectablePage() {
			m.revealSelection()
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.browser.editing && !key.Matches(msg, m.keys.Interrupt) {
			return m.editSearch(msg)
		}
		switch {
		case key.Matches(msg, m.keys.Interrupt):
			m.browser.stop()
			m.history.stop()
			return m, tea.Interrupt
		case key.Matches(msg, m.keys.Quit):
			m.browser.stop()
			m.history.stop()
			return m, tea.Quit
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
			m.history.stop()
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
			case workflowPage:
				m.page = workflowsPage
			case repositoryPage:
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
				m.page = archiveListPage
				return m.startBrowse(false)
			case m.page == repositoryPage:
				m.browser.from = repositoryPage
				m.browser.name = m.selectedTask().Repository
				m.browser.archive, m.browser.query = archive.Archive{}, ""
				m.page = archiveListPage
				return m.startBrowse(false)
			case m.page == archiveListPage && len(m.browser.rows) > 0:
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
			case workflowPage:
				m.yamlFields = []string{"workflows", m.workflowName()}
			case repositoryPage:
				m.yamlFields = []string{"repositories", m.selectedTask().Repository}
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
			m.revealSelection()
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
	if m.selectablePage() {
		m.revealSelection()
	}
	return m, nil
}

func (m Model) selectablePage() bool {
	return m.page == workflowsPage || m.page == workflowPage ||
		m.page == archivesPage || m.page == archiveListPage || m.page == fileListPage || m.page == historyPage
}

func (m Model) View() tea.View {
	content := ""
	if m.width > 0 && m.height > 0 {
		if m.width < 24 || m.height < 6 {
			content = "Resize terminal (24x6).\nq quit"
		} else {
			tabs := []string{"Home", "Workflows", "Configuration", "Archives", "History", "Help"}
			tabs[m.tab()] = lipgloss.NewStyle().Bold(true).Underline(true).Render(tabs[m.tab()])
			content = "Cairn  " + strings.Join(tabs, " | ") + "\n\n" +
				m.body.View() + "\n\n" + m.help.ShortHelpView(m.keys.ShortHelp())
		}
		content = lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}
