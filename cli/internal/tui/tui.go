// Package tui provides Cairn's read-only terminal interface.
package tui

import (
	"context"
	"fmt"
	"io"
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
	helpPage
)

// Model owns navigation and a validated configuration snapshot. It never starts
// operations or changes configuration; configuration loading belongs to the caller.
type Model struct {
	selected *config.Config
	page     page
	width    int
	height   int
	keys     keyMap
	help     help.Model
	body     viewport.Model
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
		return m, nil
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.keys.Interrupt):
			return m, tea.Interrupt
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Next, m.keys.Previous, m.keys.Help):
			if m.page == homePage {
				m.page = helpPage
			} else {
				m.page = homePage
			}
			m.body.GotoTop()
			m.refresh()
			return m, nil
		case key.Matches(msg, m.keys.Back):
			m.page = homePage
			m.body.GotoTop()
			m.refresh()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.body, cmd = m.body.Update(msg)
	return m, cmd
}

func (m *Model) refresh() {
	if m.page == helpPage {
		help := m.help
		help.SetWidth(0)
		var bindings []key.Binding
		for _, group := range m.keys.FullHelp() {
			bindings = append(bindings, group...)
		}
		m.body.SetContent("Keyboard help\n\n" + help.FullHelpView([][]key.Binding{bindings}) +
			"\n\nConfiguration is a read-only startup snapshot.\n" +
			"Quit and reopen Cairn to load another configuration.\n" +
			"Closing this interface does not stop the agent or unrelated runs.")
		return
	}
	m.body.SetContent(fmt.Sprintf("Read-only terminal interface.\n\nConfiguration: %q\n\n"+
		"Use Tab to switch between Home and Help.\n"+
		"Use the CLI for backup and repository maintenance.", m.selected.Path))
}

func (m Model) View() tea.View {
	content := ""
	if m.width > 0 && m.height > 0 {
		if m.width < 24 || m.height < 6 {
			content = "Resize terminal (24x6).\nq quit"
		} else {
			tabs := []string{"Home", "Help"}
			tabs[m.page] = lipgloss.NewStyle().Bold(true).Underline(true).Render(tabs[m.page])
			content = "Cairn  " + strings.Join(tabs, " | ") + "\n\n" +
				m.body.View() + "\n\n" + m.help.ShortHelpView(m.keys.ShortHelp())
		}
		content = lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}
