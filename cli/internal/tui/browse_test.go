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

const browseConfig = `version: 1
repositories:
  local:
    location: ./repo
    passphrase: never-display-this
  remote:
    location: ssh://backup@example.invalid/./repo
    passphrase_file: ./not-accessed
    identity_file: ./also-not-accessed
workflows:
  z-manual:
    stages:
      - tasks:
          - type: compact
            repository: local
  a-nightly:
    cron: "@daily"
    timezone: UTC
    stages:
      - tasks:
          - type: backup
            repository: local
            archive_prefix: docs
            sources: ["."]
          - type: compact
            repository: remote
      - tasks:
          - type: prune
            repository: local
            archive_prefix: docs
            keep_daily: 7
          - type: check
            repository: local
            verify_data: true
          - type: check
            repository: remote
            verify_data: false
          - type: check
            repository: remote
`

func browsingModel(t *testing.T) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "selected.yaml")
	if err := os.WriteFile(path, []byte(browseConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := config.Load(config.Options{Path: path, Explicit: true, Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	next, _ := New(selected).Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return next.(Model), path
}

func contains(t *testing.T, m Model, values ...string) {
	t.Helper()
	view := modelText(m)
	for _, value := range values {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), strings.Join(strings.Fields(value), " ")) {
			t.Fatalf("missing %q: %s", value, view)
		}
	}
	if strings.Contains(view, "never-display-this") {
		t.Fatal("secret rendered")
	}
}

func modelText(m Model) string {
	return ansi.Strip(strings.Join([]string{m.title, m.subtitle, m.notice, m.status, m.body.GetContent(), m.details.GetContent()}, "\n"))
}

func TestBrowseWorkflowsAndYAML(t *testing.T) {
	m, path := browsingModel(t)
	// Browsing must not read a modified or missing source file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m, _ = press(m, tea.KeyTab, 0)
	contains(t, m, "> a-nightly", "scheduled", "z-manual", "manual only")
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Workflow: a-nightly", "Cron: @daily", "Timezone: UTC",
		"Stage 1", "> Task 1: backup → local", "Task 2: compact → remote", "Stage 2", "Sources:", "• .")
	m, _ = press(m, 'j', 0)
	m, _ = press(m, tea.KeyEnter, 0)
	if m.page != taskPage {
		t.Fatal("opening a task skipped task details")
	}
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Repository: remote", "stage 1 / task 2", "ssh://backup@example.invalid/./repo",
		"./not-accessed", "./also-not-accessed")
	m, _ = press(m, 'y', 0)
	contains(t, m, "Section: repositories / remote", "identity_file:")
	m, _ = press(m, '?', 0)
	m, _ = press(m, '?', 0)
	if m.page != yamlPage {
		t.Fatal("help lost detail navigation")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != repositoryPage {
		t.Fatal("YAML back did not return to repository")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != taskPage {
		t.Fatal("repository back did not return to task details")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, 'j', 0)
	contains(t, m, "> Task 1: prune → local", "Keep daily: 7")
	m, _ = press(m, 'y', 0)
	contains(t, m, "Section: workflows / a-nightly", "cron:", "stages:")
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, tea.KeyEscape, 0)
	m, _ = press(m, 'j', 0)
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Workflow: z-manual", "Manual only")
	m, _ = press(m, tea.KeyTab, 0)
	contains(t, m, "Configuration source:", path, "agent's startup snapshot", "Press y")
	if strings.Contains(modelText(m), "location:") {
		t.Fatal("configuration overview embeds YAML")
	}
	m, _ = press(m, 'y', 0)
	if m.tab() != configurationPage {
		t.Fatal("full YAML lost the Configuration tab")
	}
	contains(t, m, "[redacted]", "./repo", "repositories:")
	m, _ = press(m, tea.KeyEscape, 0)
	if m.page != configurationPage {
		t.Fatal("YAML back did not return to configuration")
	}
}

func TestReload(t *testing.T) {
	m, path := browsingModel(t)
	initial := m.selected
	m, cmd := press(m, 'r', 0)
	if cmd == nil || !m.reloading {
		t.Fatal("reload not scheduled")
	}
	if _, duplicate := press(m, 'r', 0); duplicate != nil {
		t.Fatal("duplicate reload scheduled")
	}
	if err := os.WriteFile(path, []byte("version: never-display-this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if m.selected != initial || m.reloading {
		t.Fatal("failed reload replaced snapshot or stayed busy")
	}
	contains(t, m, "Reload failed:", "Previous TUI snapshot retained")
	// A missing selected file must not fall back to a valid discovery candidate.
	fallback := filepath.Join(t.TempDir(), "cairn")
	if err := os.MkdirAll(fallback, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallback, "config.yaml"), []byte(browseConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(fallback))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m, cmd = press(m, 'r', 0)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.selected != initial {
		t.Fatal("reload fell back to discovery")
	}
	if err := os.WriteFile(path, []byte("version: 1\nrepositories: {}\nworkflows: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ = press(m, tea.KeyTab, 0)
	m, _ = press(m, tea.KeyEnter, 0)
	m, _ = press(m, tea.KeyEnter, 0)
	m, _ = press(m, 'y', 0)
	m, cmd = press(m, 'r', 0)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.selected == initial || m.page != workflowsPage {
		t.Fatal("successful reload did not refresh detail navigation")
	}
	contains(t, m, "TUI configuration reloaded. Agent snapshot unchanged.", "No workflows configured.")
	for _, code := range []rune{tea.KeyEnter, 'y', 'j', 'k'} {
		m, _ = press(m, code, 0)
		if m.page != workflowsPage {
			t.Fatal("empty workflow list opened a detail")
		}
	}
}

func TestBrowseCheckTasks(t *testing.T) {
	m, _ := browsingModel(t)
	m, _ = press(m, tea.KeyTab, 0)
	m, _ = press(m, tea.KeyEnter, 0)
	for range 3 {
		m, _ = press(m, 'j', 0)
	}
	contains(t, m, "> Task 2: check → local", "Operation: check", "Verify data: true")
	m, _ = press(m, 'j', 0)
	contains(t, m, "> Task 3: check → remote", "Verify data: false")
	m, _ = press(m, 'j', 0)
	contains(t, m, "> Task 4: check → remote")
	taskYAML := m.selected.SanitizedYAML("workflows", "a-nightly", "stages", "1", "tasks", "3")
	if strings.Contains(taskYAML, "verify_data") {
		t.Fatal("browsing invented an omitted verify_data setting")
	}
	m, _ = press(m, tea.KeyEnter, 0)
	contains(t, m, "Repository: remote", "Passphrase file: ./not-accessed")
}

func TestSelectionRemainsVisible(t *testing.T) {
	m := sizedModel()
	for i := range 40 {
		if m.selected.Workflows == nil {
			m.selected.Workflows = map[string]config.Workflow{}
		}
		m.selected.Workflows[fmt.Sprintf("workflow-%02d-with-a-long-name", i)] = config.Workflow{}
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 10})
	m = next.(Model)
	m, _ = press(m, tea.KeyTab, 0)
	for range 39 {
		m, _ = press(m, 'j', 0)
		if !strings.Contains(ansi.Strip(m.body.View()), "> workflow-") {
			t.Fatalf("selection scrolled out of view: %s", m.body.View())
		}
	}
	for range 39 {
		m, _ = press(m, 'k', 0)
		if !strings.Contains(ansi.Strip(m.body.View()), "> workflow-") {
			t.Fatalf("selection scrolled out of view: %s", m.body.View())
		}
	}
}

func TestReloadFromHelpDropsStaleDetails(t *testing.T) {
	m, path := browsingModel(t)
	for _, code := range []rune{tea.KeyTab, tea.KeyEnter, tea.KeyEnter, '?'} {
		m, _ = press(m, code, 0)
	}
	if err := os.WriteFile(path, []byte("version: 1\nrepositories: {}\nworkflows: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, cmd := press(m, 'r', 0)
	next, _ := m.Update(cmd())
	m = next.(Model)
	m, _ = press(m, '?', 0)
	if m.page != homePage {
		t.Fatal("help returned to stale repository details after reload")
	}
}

func TestBrowseResizes(t *testing.T) {
	m, _ := browsingModel(t)
	for _, target := range []page{homePage, workflowsPage, workflowPage, repositoryPage, configurationPage, helpPage, yamlPage} {
		m.page = target
		m.yamlFields = []string{"workflows", "a-nightly"}
		for _, size := range []tea.WindowSizeMsg{
			{Width: 1, Height: 1}, {Width: 0, Height: 0}, {Width: 24, Height: 6},
			{Width: 40, Height: 10}, {Width: 100, Height: 40},
		} {
			next, _ := m.Update(size)
			m = next.(Model)
			if m.page != target {
				t.Fatal("resize changed page")
			}
			content := m.View().Content
			if lipgloss.Width(content) > size.Width || lipgloss.Height(content) > max(1, size.Height) {
				t.Fatalf("%v overflows %dx%d: %q", target, size.Width, size.Height, content)
			}
			if strings.Contains(content, "never-display-this") {
				t.Fatal("resize exposed a secret")
			}
		}
	}
}
