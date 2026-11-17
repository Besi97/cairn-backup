package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Next, Previous, Help, Back, Scroll, Page, Quit, Interrupt, Open, YAML, Reload, Refresh key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Next:      key.NewBinding(key.WithKeys("tab", "right", "l"), key.WithHelp("tab/→/l", "next view")),
		Previous:  key.NewBinding(key.WithKeys("shift+tab", "left", "h"), key.WithHelp("shift+tab/←/h", "previous view")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Scroll:    key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓/k/j", "select / scroll")),
		Page:      key.NewBinding(key.WithKeys("pgup", "pgdown", "b", "f"), key.WithHelp("pgup/pgdn/b/f", "scroll page")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Interrupt: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "interrupt")),
		Open:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open selection")),
		YAML:      key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "related YAML")),
		Reload:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload TUI config")),
		Refresh:   key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "refresh history")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit, k.Next, k.Reload}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Next, k.Previous, k.Help, k.Back}, {k.Open, k.YAML, k.Reload, k.Refresh}, {k.Scroll, k.Page, k.Quit, k.Interrupt}}
}
