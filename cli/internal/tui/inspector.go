package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Besi97/cairn-backup/cli/internal/config"
	"github.com/charmbracelet/x/ansi"
	"go.yaml.in/yaml/v3"
)

type inspectionItem struct {
	name, summary string
}

func counted(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func (m Model) itemList(items []inspectionItem, cursor int, empty string) string {
	if len(items) == 0 {
		return mutedStyle.Render(empty)
	}
	cursor = max(0, min(cursor, len(items)-1))
	count := max(1, (m.body.Height()-2)/3)
	start := max(0, min(cursor-count/2, len(items)-count))
	end := min(len(items), start+count)
	var rows []string
	if m.body.Height() >= 5 {
		rows = append(rows, mutedStyle.Render(fmt.Sprintf("%d / %d", cursor+1, len(items))), "")
	}
	for i := start; i < end; i++ {
		item := items[i]
		line := "  " + clipped(item.name, m.body.Width()-2)
		if i == cursor {
			line = selectedStyle.Width(m.body.Width()).Render("> " + clipped(item.name, m.body.Width()-2))
		}
		rows = append(rows, line)
		if m.body.Height() >= 5 {
			rows = append(rows, "  "+clipped(item.summary, m.body.Width()-2), "")
		}
	}
	return strings.TrimRight(strings.Join(rows, "\n"), "\n")
}

// Use only the sanitized, unresolved snapshot, never resolved credential values.
func (m Model) configuredFields(fields ...string) string {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(m.selected.SanitizedYAML(fields...)), &doc) != nil ||
		len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "No configured fields."
	}
	node := doc.Content[0]
	var rows []string
	for i := 0; i < len(node.Content); i += 2 {
		label, value := node.Content[i].Value, node.Content[i+1]
		if len(fields) == 6 && fields[0] == "workflows" && fields[4] == "tasks" &&
			(label == "type" || label == "repository") {
			continue
		}
		label = strings.ToUpper(label[:1]) + strings.ReplaceAll(label[1:], "_", " ")
		if value.Kind == yaml.SequenceNode {
			rows = append(rows, mutedStyle.Render(label+":"))
			for _, child := range value.Content {
				rows = append(rows, "  • "+child.Value)
			}
		} else if value.Kind == yaml.ScalarNode {
			rows = append(rows, field(label, value.Value))
		}
	}
	if len(rows) == 0 {
		return "No additional settings configured."
	}
	return strings.Join(rows, "\n\n")
}

func (m Model) workflowDetail(name string) string {
	w := m.selected.Workflows[name]
	count := 0
	for _, stage := range w.Stages {
		count += len(stage.Tasks)
	}
	return section("Workflow: "+config.DisplayText(name), m.schedule(w)) + "\n\n" +
		section("Execution plan", counted(len(w.Stages), "stage", "stages")+" · "+counted(count, "task", "tasks")+
			"\n\nStages run in order.\nTasks within a stage run concurrently.") +
		"\n\n" + mutedStyle.Render("Enter inspects tasks. y opens workflow YAML.")
}

func (m Model) taskDetail() string {
	t := m.selectedTask()
	if t.stage == 0 {
		return "No tasks configured."
	}
	return section(fmt.Sprintf("Stage %d / Task %d", t.stage, t.task),
		field("Operation", config.DisplayText(t.Type))+"\n\n"+field("Repository", config.DisplayText(t.Repository))) +
		"\n\n" + section("Task settings", m.configuredFields("workflows", m.workflowName(), "stages",
		strconv.Itoa(t.stage-1), "tasks", strconv.Itoa(t.task-1))) +
		"\n\n" + mutedStyle.Render("Configured values only. Enter inspects the repository.")
}

func (m Model) repositoryDetail(name string) string {
	return section("Repository: "+config.DisplayText(name), m.configuredFields("repositories", name)) +
		"\n\n" + mutedStyle.Render("Paths are unresolved. Inline credentials are redacted.\nCredential files are never opened for display.") +
		"\n\n" + mutedStyle.Render("Enter lists archives and may contact this repository.")
}

func (m *Model) refresh() {
	m.sizePanes()
	m.title, m.subtitle, m.notice = "", "", ""
	var content, detail, identity string
	switch m.page {
	case homePage:
		m.title, m.subtitle = "Overview", "Your configuration at a glance"
		content = section("Configuration:", config.DisplayText(m.selected.Path)) + "\n\n" +
			section("Available to inspect", counted(len(m.selected.Workflows), "workflow", "workflows")+"\n\n"+
				counted(len(m.selected.Repositories), "repository", "repositories")+"\n\nLocal run history") +
			"\n\n" + section("Browse without changing anything", "Use Tab to choose a view.\n\nInspect workflow plans, browse archive metadata,\nor review recorded run outcomes.") +
			"\n\n" + mutedStyle.Render("The TUI configuration is a separate snapshot.\nNo live agent state is observed. Use the CLI to run operations.")
	case workflowsPage:
		m.title, m.subtitle = "Workflows", "Choose a workflow to inspect its schedule and execution plan"
		var items []inspectionItem
		for _, name := range m.workflowNames() {
			w, schedule := m.selected.Workflows[name], "scheduled"
			if w.Cron == "" {
				schedule = "manual only"
			}
			items = append(items, inspectionItem{config.DisplayText(name), mutedStyle.Render(schedule + " · " + counted(len(w.Stages), "stage", "stages"))})
		}
		content = m.itemList(items, m.workflow, "No workflows configured.")
		if len(items) > 0 {
			detail, identity = m.workflowDetail(m.workflowName()), m.workflowName()
		}
	case workflowPage:
		name := m.workflowName()
		m.title, m.subtitle = "Workflow: "+config.DisplayText(name), strings.ReplaceAll(m.schedule(m.selected.Workflows[name]), "\n", " · ")
		var items []inspectionItem
		for _, t := range m.tasks() {
			items = append(items, inspectionItem{
				fmt.Sprintf("Task %d: %s → %s", t.task, config.DisplayText(t.Type), config.DisplayText(t.Repository)),
				mutedStyle.Render(fmt.Sprintf("Stage %d", t.stage))})
		}
		content = m.itemList(items, m.task, "No tasks configured.")
		detail, identity = m.taskDetail(), fmt.Sprintf("%s/%d", name, m.task)
	case repositoryPage:
		t := m.selectedTask()
		m.title = "Repository: " + config.DisplayText(m.repositoryName())
		m.subtitle = fmt.Sprintf("Referenced by %s / stage %d / task %d", config.DisplayText(m.workflowName()), t.stage, t.task)
		if m.repositoryFrom == archivesPage {
			m.subtitle = "Configured repository · Enter lists archives and may contact it"
		}
		content = m.repositoryDetail(m.repositoryName())
	case taskPage:
		t := m.selectedTask()
		m.title = fmt.Sprintf("Task %d: %s", t.task, config.DisplayText(t.Type))
		m.subtitle = fmt.Sprintf("Workflow: %s / Stage %d", config.DisplayText(m.workflowName()), t.stage)
		content = m.taskDetail()
	case configurationPage:
		m.title, m.subtitle = "Configuration", "Read-only TUI snapshot · r reloads the selected file"
		content = section("Configuration source:", config.DisplayText(m.selected.Path)) +
			"\n\n" + section("Contents", counted(len(m.selected.Workflows), "workflow", "workflows")+"\n\n"+
			counted(len(m.selected.Repositories), "repository", "repositories")+fmt.Sprintf("\n\nSchema version %d", m.selected.Version)) +
			"\n\n" + section("Snapshot boundaries", "Reload does not change the agent's startup snapshot.\n\nConfigured paths are shown without resolving them.\nInline credentials are redacted; credential files are not opened.") +
			"\n\n" + section("Inspect the source", "Press y to open normalized, sanitized YAML.\nComments and anchors are not retained.")
	case yamlPage:
		m.title, m.subtitle = "Configuration YAML", "Normalized configured values · inline credentials redacted"
		content = field("Configuration source", config.DisplayText(m.selected.Path)) + "\n" +
			field("Section", config.DisplayText(strings.Join(m.yamlFields, " / "))) + "\n\n" + m.selected.SanitizedYAML(m.yamlFields...)
	case archivesPage, archiveListPage, archiveDetailPage, fileListPage, filePage:
		content, detail, identity = m.archiveInspection()
	case historyPage, runPage:
		content, detail, identity = m.historyInspection()
	case helpPage:
		m.title, m.subtitle = "Keyboard help", "Browse and inspect · no execution or configuration editing"
		content = section("Navigation", "Tab / Shift+Tab    next / previous tab\n← → / h l          previous / next tab\n↑ ↓ / k j          select an item or scroll\nEnter              open selection\nEsc                back\n?                  toggle help\nq / Ctrl+C         exit") +
			"\n\n" + section("Inspector", "Wide terminals show a list beside its details.\nPage Up/Down (b/f) scrolls the detail pane.\nEnter opens the next level; Esc returns.\nNarrow terminals show one level at a time.") +
			"\n\n" + section("Configuration", "y opens workflow, repository, or full configuration YAML.\nr reloads the selected file into this TUI only.\nA failed reload keeps the previous snapshot.\nNo automatic rereads. No live agent controls.") +
			"\n\n" + section("Archives", "/ edits a literal, case-sensitive file path search.\nEnter scans the whole archive, not just retained rows.\nEsc cancels a scan when leaving its list.\nBrowsing may open credentials and contact repositories.\nListings retain at most 1,000 rows / 8 MiB.\nLossy display paths are never exact restore selectors.") +
			"\n\n" + section("History", "g rereads local history; no automatic refresh.\nOnly run summaries and task totals are retained.\nIncomplete does not mean active.\nOwner and lock evidence is momentary, not live agent state.\nConfiguration identities do not reveal content differences.") +
			"\n\nClosing this interface does not stop the agent or unrelated runs."
	}
	if m.page != yamlPage {
		content = ansi.Wrap(content, m.body.Width(), "")
	}
	m.body.SetContent(content)
	if detail == "" && m.split() {
		detail = mutedStyle.Render("Nothing to inspect yet.")
	}
	m.details.SetContent(ansi.Wrap(detail, m.details.Width(), ""))
	if m.selectablePage() {
		m.body.GotoTop()
	}
	key := fmt.Sprintf("%d/%s", m.page, identity)
	if key != m.detailKey {
		m.details.GotoTop()
		m.detailKey = key
	}
}
