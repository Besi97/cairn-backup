package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	accentStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("4")).Bold(true)
)

const inspectorWidth = 100

func heading(value string) string { return accentStyle.Render(value) }

func section(title, content string) string {
	return heading(title) + "\n\n" + content
}

func field(label, value string) string {
	if value == "" {
		value = "—"
	}
	return mutedStyle.Render(fmt.Sprintf("%-18s", label+":")) + " " + value
}

func state(value string) string {
	color := "8"
	switch value {
	case "success", "Complete.":
		color = "2"
	case "warning", "incomplete", "partial", "interrupted", "skipped", "TRUNCATED":
		color = "3"
	case "failure":
		color = "1"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true).Render(value)
}

func clipped(value string, width int) string {
	return ansi.Truncate(value, max(1, width), "…")
}

func (m Model) split() bool {
	return m.width >= inspectorWidth && m.height >= 16 && m.selectablePage()
}

func (m Model) paneHeight() int {
	if m.height < 16 {
		return max(3, m.height-7)
	}
	return m.height - 8
}

func (m *Model) sizePanes() {
	width, height := max(1, m.width-2), m.paneHeight()
	left, right := width, width
	if m.split() {
		left = max(30, width*2/5)
		right = width - left - 2
	}
	m.body.SetWidth(max(1, left-4))
	m.body.SetHeight(max(1, height-2))
	m.details.SetWidth(max(1, right-4))
	m.details.SetHeight(max(1, height-2))
}

func panel(body viewport.Model, width, height int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).Width(width).Height(height).
		Render(body.View())
}

func (m Model) footer() string {
	keys := "↑↓ select  enter open"
	switch m.page {
	case homePage:
		keys = "tab browse  r reload config"
	case configurationPage:
		keys = "y view YAML  r reload config  ↑↓ scroll"
	case workflowsPage:
		keys += "  y YAML"
	case workflowPage:
		keys = "↑↓ select task  enter details  y workflow YAML"
	case archivesPage:
		keys = "↑↓ select  enter list archives  y YAML"
	case archiveListPage:
		keys = "↑↓ select  enter details"
	case archiveDetailPage:
		keys = "enter browse files  ↑↓ scroll  esc back"
	case fileListPage:
		keys = "↑↓ select  enter details  / search"
	case historyPage:
		keys = "↑↓ select  enter summary  g refresh"
	case runPage:
		keys = "↑↓ scroll  g refresh  esc back"
	case repositoryPage:
		keys = "enter list archives  y YAML  ↑↓ scroll"
	case taskPage:
		keys = "enter repository (Archives)  y workflow YAML  ↑↓ scroll"
	default:
		keys = "↑↓ scroll  esc back"
	}
	if m.split() {
		keys = "↑↓ select  pgup/pgdn details  enter open"
		if m.page == workflowPage {
			keys = "↑↓ select  pgup/pgdn scroll  enter task details"
		}
		switch m.page {
		case workflowsPage, workflowPage, archivesPage:
			keys += "  y YAML"
		case fileListPage:
			keys += "  / search"
		case historyPage:
			keys += "  g refresh"
		}
	}
	if m.browser.editing {
		keys = "enter search whole archive  esc discard"
	}
	return "? help  q quit  ·  " + keys
}

func (m Model) inspectorView() tea.View {
	content := ""
	if m.width > 0 && m.height > 0 {
		if m.width < 24 || m.height < 10 {
			content = "Resize terminal (24×10).\nq quit"
		} else {
			width, height := m.width-2, m.paneHeight()
			tabs := []string{"Home", "Workflows", "Configuration", "Archives", "History", "Help"}
			for i := range tabs {
				if page(i) == m.tab() {
					tabs[i] = selectedStyle.Render(" " + tabs[i] + " ")
				} else {
					tabs[i] = mutedStyle.Render(" " + tabs[i] + " ")
				}
			}
			navigation := strings.Join(tabs, " ")
			if lipgloss.Width(navigation) > width {
				names := []string{"Home", "Workflows", "Configuration", "Archives", "History", "Help"}
				navigation = selectedStyle.Render(" "+names[m.tab()]+" ") + mutedStyle.Render("  ‹ shift+tab · tab ›")
			}
			panes := panel(m.body, width, height)
			if m.split() {
				left := max(30, width*2/5)
				panes = lipgloss.JoinHorizontal(lipgloss.Top,
					panel(m.body, left, height), "  ", panel(m.details, width-left-2, height))
			}
			notice := m.notice
			if m.status != "" {
				notice = m.status
			}
			if notice == "" {
				notice = "Read-only · no operations are started from this interface"
			}
			scroll := m.body
			if m.split() {
				scroll = m.details
			}
			if scroll.TotalLineCount() > scroll.Height() && m.status == "" && m.notice == "" {
				notice = fmt.Sprintf("Details %d%% · pgup/pgdn scroll · read-only", int(scroll.ScrollPercent()*100))
			}
			gap := "\n\n"
			if m.height < 16 {
				gap = "\n"
			}
			content = clipped(heading("CAIRN")+mutedStyle.Render("  /  read-only inspector"), width) + "\n" +
				clipped(navigation, width) + gap +
				clipped(heading(m.title), width) + "\n" +
				clipped(mutedStyle.Render(m.subtitle), width) + "\n\n" +
				panes + "\n" + clipped(notice, width) + "\n" +
				clipped(mutedStyle.Render(m.footer()), width)
			content = lipgloss.NewStyle().PaddingLeft(1).Render(content)
		}
		if os.Getenv("NO_COLOR") != "" {
			content = ansi.Strip(content)
		}
		content = lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(content)
	}
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}
