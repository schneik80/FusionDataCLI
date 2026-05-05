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
	Insert      key.Binding
	Refresh     key.Binding
	Download    key.Binding
	Hub         key.Binding
	About       key.Binding
	Theme       key.Binding
	Debug       key.Binding
	Mouse       key.Binding
	Quit        key.Binding
	// Details-pane tab switching
	TabSelect key.Binding // 1/2/3/4 — direct jump to a tab
	TabNext   key.Binding // Tab     — cycle forward
	TabPrev   key.Binding // S-Tab   — cycle back
	// Pins
	PinToggle key.Binding // P             — pin/unpin current item
	PinsOpen  key.Binding // p             — open pins overlay
	PinDelete key.Binding // delete/bksp   — remove selected pin (overlay only)
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
		key.WithKeys("left"),
		key.WithHelp("←", "back"),
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
	Insert: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "insert in Fusion"),
	),
	Refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	Download: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "download STEP"),
	),
	Hub: key.NewBinding(
		key.WithKeys("h"),
		key.WithHelp("h", "switch hub"),
	),
	About: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "about"),
	),
	Theme: key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "cycle theme"),
	),
	Debug: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "debug log"),
	),
	Mouse: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "toggle mouse"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	TabSelect: key.NewBinding(
		key.WithKeys("1", "2", "3", "4"),
		key.WithHelp("1-4", "details tab"),
	),
	TabNext: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "next details tab"),
	),
	TabPrev: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev details tab"),
	),
	PinToggle: key.NewBinding(
		key.WithKeys("P"),
		key.WithHelp("P", "pin/unpin item"),
	),
	PinsOpen: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "open pins"),
	),
	PinDelete: key.NewBinding(
		key.WithKeys("delete", "backspace"),
		key.WithHelp("del", "remove pin"),
	),
}
