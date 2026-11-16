package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Besi97/cairn-backup/cli/internal/config"
)

func sizedModel() Model {
	m := New(&config.Config{Path: "/config/cairn.yaml"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Model)
}

func press(m Model, code rune, mod tea.KeyMod) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return next.(Model), cmd
}

func TestNavigation(t *testing.T) {
	for _, key := range []struct {
		name string
		code rune
		mod  tea.KeyMod
		want page
	}{
		{"tab", tea.KeyTab, 0, workflowsPage}, {"back tab", tea.KeyTab, tea.ModShift, helpPage},
		{"right", tea.KeyRight, 0, workflowsPage}, {"left", tea.KeyLeft, 0, helpPage},
		{"vim right", 'l', 0, workflowsPage}, {"vim left", 'h', 0, helpPage}, {"help", '?', 0, helpPage},
	} {
		t.Run(key.name, func(t *testing.T) {
			m := sizedModel()
			m, cmd := press(m, key.code, key.mod)
			if cmd != nil || m.page != key.want {
				t.Fatalf("page = %v, want %v", m.page, key.want)
			}
			m, _ = press(m, tea.KeyEscape, 0)
			if m.page != homePage || !strings.Contains(m.View().Content, "Configuration:") {
				t.Fatal("did not return to Home")
			}
			m, _ = press(m, '?', 0)
			m, _ = press(m, tea.KeyEscape, 0)
			if m.page != homePage {
				t.Fatal("escape did not return home")
			}
		})
	}
}

func TestQuitAndInterrupt(t *testing.T) {
	m := sizedModel()
	if m.Init() != nil {
		t.Fatal("shell must not start background work")
	}
	_, cmd := press(m, 'q', 0)
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
	_, cmd = press(m, 'c', tea.ModCtrl)
	if _, ok := cmd().(tea.InterruptMsg); !ok {
		t.Fatal("ctrl+c did not interrupt")
	}
	_, cmd = press(m, 'x', 0)
	if cmd != nil {
		t.Fatal("unbound key started work")
	}
}

func TestResizeAndScroll(t *testing.T) {
	m := sizedModel()
	m, _ = press(m, '?', 0)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 40, Height: 10}, {Width: 24, Height: 6},
		{Width: 1, Height: 1}, {Width: 0, Height: 0},
		{Width: 120, Height: 40}, {Width: 80, Height: 12},
	} {
		next, _ := m.Update(size)
		m = next.(Model)
		view := m.View()
		if !view.AltScreen {
			t.Fatal("shell must restore the original screen on exit")
		}
		if lipgloss.Width(view.Content) > size.Width || lipgloss.Height(view.Content) > max(1, size.Height) {
			t.Fatalf("%dx%d view overflows: %q", size.Width, size.Height, view.Content)
		}
		if m.page != helpPage {
			t.Fatal("resize lost navigation")
		}
	}
	for range 20 {
		m, _ = press(m, tea.KeyDown, 0)
	}
	if m.body.YOffset() == 0 || !strings.Contains(m.View().Content, "unrelated runs") {
		t.Fatalf("help did not scroll to bottom: %q", m.View().Content)
	}
	m, _ = press(m, tea.KeyPgUp, 0)
	if m.body.YOffset() >= m.body.TotalLineCount()-m.body.Height() {
		t.Fatal("page up did not scroll")
	}
	m, _ = press(m, tea.KeyEscape, 0)
	if m.body.YOffset() != 0 {
		t.Fatal("changing views did not reset scroll")
	}
}

func TestConfigurationIsNotRendered(t *testing.T) {
	secret := "never-display-this"
	selected := &config.Config{
		Path: "/config/escape\x1b[31m\n.yaml",
		Repositories: map[string]config.Repository{
			secret: {Location: secret, Passphrase: &secret, PassphraseFile: secret, IdentityFile: secret},
		},
	}
	m := New(selected)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	for range 2 {
		view := m.View().Content
		if strings.Contains(view, secret) || strings.Contains(view, "\x1b[31m") {
			t.Fatalf("configuration value or unsafe path rendered: %q", view)
		}
		m, _ = press(m, '?', 0)
	}
}
