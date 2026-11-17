package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
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
	if m.page == repositoryPage || (m.page == yamlPage && m.yamlFrom == repositoryPage) {
		return archivesPage
	}
	if m.page == yamlPage {
		switch m.yamlFrom {
		case configurationPage, archivesPage:
			return m.yamlFrom
		default:
			return workflowsPage
		}
	}
	if m.page == runPage {
		return historyPage
	}
	if m.page == archiveListPage || m.page == archiveDetailPage || m.page == fileListPage || m.page == filePage {
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

func (m Model) repositoryName() string {
	if m.repositoryFrom == archivesPage {
		return m.browser.name
	}
	return m.selectedTask().Repository
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
