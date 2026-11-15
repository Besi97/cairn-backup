package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Next, Previous, Help, Back, Scroll, Page, Quit, Interrupt key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Next:      key.NewBinding(key.WithKeys("tab", "right", "l"), key.WithHelp("tab/→/l", "next view")),
		Previous:  key.NewBinding(key.WithKeys("shift+tab", "left", "h"), key.WithHelp("shift+tab/←/h", "previous view")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "toggle help")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "home")),
		Scroll:    key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑/↓/k/j", "scroll")),
		Page:      key.NewBinding(key.WithKeys("pgup", "pgdown", "b", "f"), key.WithHelp("pgup/pgdn/b/f", "scroll page")),
		Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Interrupt: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "interrupt")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit, k.Next}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Next, k.Previous, k.Help, k.Back}, {k.Scroll, k.Page, k.Quit, k.Interrupt}}
}
