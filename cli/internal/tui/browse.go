package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

type reloadMsg struct{ selected *config.Config }

func reload(path string) tea.Cmd {
	return func() tea.Msg {
		options, err := config.Environment()
		if err != nil {
			return reloadMsg{}
		}
		options.Path, options.Explicit = path, true
		selected, err := config.Load(options)
		if err != nil {
			return reloadMsg{}
		}
		return reloadMsg{selected}
	}
}

func (m Model) tab() page {
	if m.page == runPage {
		return historyPage
	}
	if m.page == archiveListPage || m.page == fileListPage || m.page == filePage {
		return archivesPage
	}
	if m.page > helpPage {
		return workflowsPage
	}
	return m.page
}

func (m Model) workflowNames() []string {
	names := make([]string, 0, len(m.selected.Workflows))
	for name := range m.selected.Workflows {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (m Model) workflowName() string {
	names := m.workflowNames()
	if m.workflow < 0 || m.workflow >= len(names) {
		return ""
	}
	return names[m.workflow]
}

type taskPosition struct {
	stage, task int
	config.Task
}

func (m Model) tasks() []taskPosition {
	var tasks []taskPosition
	for si, stage := range m.selected.Workflows[m.workflowName()].Stages {
		for ti, task := range stage.Tasks {
			tasks = append(tasks, taskPosition{si + 1, ti + 1, task})
		}
	}
	return tasks
}

func (m Model) selectedTask() taskPosition {
	tasks := m.tasks()
	if m.task < 0 || m.task >= len(tasks) {
		return taskPosition{}
	}
	return tasks[m.task]
}

func (m Model) schedule(workflow config.Workflow) string {
	zone := config.DisplayText(workflow.Timezone)
	if zone == "" {
		zone = "agent process local timezone (not observed)"
	}
	if workflow.Cron == "" {
		return "Manual only"
	}
	return fmt.Sprintf("Cron: %s\nTimezone: %s", config.DisplayText(workflow.Cron), zone)
}

func (m *Model) refresh() {
	var text strings.Builder
	switch m.page {
	case historyPage, runPage:
		text.WriteString(m.historyText())
	case archivesPage, archiveListPage, fileListPage, filePage:
		text.WriteString(m.browserText())
	case homePage:
		fmt.Fprintf(&text, "Read-only terminal interface.\n\nConfiguration: %s\n\n"+
			"%d workflows · %d repositories\n\n"+
			"Tab: browse workflows, configuration, archives and history. ?: keyboard help.\n"+
			"r: reload the selected file into this TUI only.\n"+
			"No agent state is observed; its startup snapshot is separate.\n"+
			"Use the CLI for backup and repository maintenance.",
			config.DisplayText(m.selected.Path), len(m.selected.Workflows), len(m.selected.Repositories))
	case workflowsPage:
		text.WriteString("Workflows\n\n↑/↓ select · enter workflow\n\n")
		names := m.workflowNames()
		if len(names) == 0 {
			text.WriteString("No workflows configured.")
		}
		for i, name := range names {
			marker := "  "
			if i == m.workflow {
				marker = "> "
			}
			schedule := "scheduled"
			if m.selected.Workflows[name].Cron == "" {
				schedule = "manual only"
			}
			fmt.Fprintf(&text, "%s%s (%s)\n", marker, config.DisplayText(name), schedule)
		}
	case workflowPage:
		name := m.workflowName()
		workflow := m.selected.Workflows[name]
		fmt.Fprintf(&text, "Workflow: %s\n%s\n\n"+
			"↑/↓ select task · enter repository · y workflow YAML · esc back\n"+
			"Stages run in order; tasks within a stage run concurrently.\n\n",
			config.DisplayText(name), m.schedule(workflow))
		index := 0
		for si, stage := range workflow.Stages {
			fmt.Fprintf(&text, "Stage %d\n", si+1)
			for ti, task := range stage.Tasks {
				marker := "  "
				if index == m.task {
					marker = "> "
				}
				fmt.Fprintf(&text, "%sTask %d: %s → %s\n", marker, ti+1,
					config.DisplayText(task.Type), config.DisplayText(task.Repository))
				index++
			}
		}
		task := m.selectedTask()
		fmt.Fprintf(&text, "\nSelected task YAML (stage %d, task %d; configured values):\n%s",
			task.stage, task.task, m.selected.SanitizedYAML("workflows", name, "stages",
				fmt.Sprint(task.stage-1), "tasks", fmt.Sprint(task.task-1)))
	case repositoryPage:
		task := m.selectedTask()
		fmt.Fprintf(&text, "Repository: %s\nReferenced by %s / stage %d / task %d\n\n"+
			"enter list archives · y repository YAML · esc back\n\nConfigured values (paths are unresolved):\n%s",
			config.DisplayText(task.Repository), config.DisplayText(m.workflowName()), task.stage, task.task,
			m.selected.SanitizedYAML("repositories", task.Repository))
	case configurationPage:
		fmt.Fprintf(&text, "Configuration source: %s\n\n"+
			"Read-only TUI snapshot · r reload selected file\n"+
			"Reload does not change the agent's startup snapshot.\n"+
			"Normalized YAML; configured paths, not resolved paths.\n"+
			"Comments/anchors omitted; inline credentials redacted.\n"+
			"Credential files are never opened for display.\n\n%s",
			config.DisplayText(m.selected.Path), m.selected.SanitizedYAML())
	case yamlPage:
		fmt.Fprintf(&text, "Configuration source: %s\nSection: %s\n\n"+
			"Normalized YAML · configured values · esc back\n\n%s",
			config.DisplayText(m.selected.Path), config.DisplayText(strings.Join(m.yamlFields, " / ")),
			m.selected.SanitizedYAML(m.yamlFields...))
	case helpPage:
		help := m.help
		help.SetWidth(0)
		var bindings []key.Binding
		for _, group := range m.keys.FullHelp() {
			bindings = append(bindings, group...)
		}
		text.WriteString("Keyboard help\n\n" + help.FullHelpView([][]key.Binding{bindings}) +
			"\n\nWorkflows: select a workflow, then a task; enter follows its repository.\n" +
			"y opens the workflow or repository YAML. Esc returns.\n" +
			"Page keys scroll details; arrows select workflows/tasks.\n" +
			"Archives: choose a repository and archive; enter opens file metadata.\n" +
			"/ edits a literal, case-sensitive file path search; Enter scans the entire archive.\n" +
			"Esc cancels a scan when leaving its list. Browsing may open credentials and contact repositories.\n" +
			"Listings retain at most 1,000 rows / 8 MiB. Truncation and partial results are labelled.\n" +
			"Names and paths are lossy display text, never exact restore selectors.\n" +
			"History: retained run summaries and task totals, not per-stage/task results.\n" +
			"Enter opens a summary; g rereads local history; Esc returns. No automatic refresh.\n" +
			"Recorded outcomes are separate from momentary owner/lock evidence, not live agent state.\n" +
			"Configuration is a read-only TUI snapshot, not live agent state.\n" +
			"r validates the selected file and replaces only this TUI snapshot.\n" +
			"A failed reload keeps the previous snapshot. No automatic rereads.\n" +
			"Closing this interface does not stop the agent or unrelated runs.")
	}
	if m.status != "" {
		m.body.SetContent(m.status + "\n\n" + text.String())
	} else {
		m.body.SetContent(text.String())
	}
}

func (m *Model) revealSelection() {
	// Wrapped lines are counted by the viewport, including long names.
	lines := strings.Split(m.body.GetContent(), "\n")
	line := 0
	for _, content := range lines {
		if strings.HasPrefix(content, "> ") {
			if line < m.body.YOffset() {
				m.body.SetYOffset(line)
			} else if line >= m.body.YOffset()+m.body.Height() {
				m.body.SetYOffset(line - m.body.Height() + 1)
			}
			return
		}
		line += max(1, (lipgloss.Width(content)+max(1, m.body.Width())-1)/max(1, m.body.Width()))
	}
}
