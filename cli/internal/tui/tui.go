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
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

type page int

const (
	homePage page = iota
	workflowsPage
	configurationPage
	helpPage
	workflowPage
	repositoryPage
	yamlPage
)

// Model owns navigation and a validated TUI snapshot, separate from any agent.
// It never starts operations or changes configuration files.
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
	return Model{selected: selected, keys: newKeyMap(), help: h, body: body}
}

// Run uses only the supplied streams and leaves signal ownership with the CLI.
func Run(ctx context.Context, input io.Reader, output io.Writer, selected *config.Config) error {
	_, err := tea.NewProgram(New(selected), tea.WithContext(ctx),
		tea.WithInput(input), tea.WithOutput(output), tea.WithoutSignalHandler()).Run()
	return err
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		m.help.SetWidth(m.width)
		m.body.SetWidth(m.width)
		m.body.SetHeight(max(0, m.height-4))
		m.refresh()
		if m.page == workflowsPage || m.page == workflowPage {
			m.revealSelection()
		}
		return m, nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Interrupt):
			return m, tea.Interrupt
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
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
			direction := 1
			if key.Matches(msg, m.keys.Previous) {
				direction = -1
			}
			m.page = (m.tab() + page(direction) + 4) % 4
			return m.changed()
		case key.Matches(msg, m.keys.Back):
			switch m.page {
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
			case m.page == workflowsPage && len(m.workflowNames()) > 0:
				m.page, m.task = workflowPage, 0
			case m.page == workflowPage && len(m.tasks()) > 0:
				m.page = repositoryPage
			default:
				return m, nil
			}
			return m.changed()
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
	if m.page == workflowsPage || m.page == workflowPage {
		m.revealSelection()
	}
	return m, nil
}

func (m Model) View() tea.View {
	content := ""
	if m.width > 0 && m.height > 0 {
		if m.width < 24 || m.height < 6 {
			content = "Resize terminal (24x6).\nq quit"
		} else {
			tabs := []string{"Home", "Workflows", "Configuration", "Help"}
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
