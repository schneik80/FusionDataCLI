package ui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	Left        key.Binding
	Right       key.Binding
	Enter       key.Binding
	Open        key.Binding
	OpenDesktop key.Binding
	OpenViewer  key.Binding
	Refresh     key.Binding
	Details     key.Binding
	Debug       key.Binding
	Quit        key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "back"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "enter"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "enter"),
	),
	Open: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open in browser"),
	),
	OpenDesktop: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "open in Fusion"),
	),
	OpenViewer: key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "view in browser"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	Details: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "details"),
	),
	Debug: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "debug log"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}
