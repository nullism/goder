package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines the key bindings for the application.
type KeyMap struct {
	Quit       key.Binding
	Submit     key.Binding
	ToggleMode key.Binding
	Cancel     key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	NewLine    key.Binding
	Help       key.Binding
	Settings   key.Binding
}

// DefaultKeyMap returns the default set of key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
		Submit: key.NewBinding(
			key.WithKeys("ctrl+s"),
			key.WithHelp("ctrl+s", "submit"),
		),
		ToggleMode: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "toggle mode"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("up", "pgup"),
			key.WithHelp("up/pgup", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("down", "pgdown"),
			key.WithHelp("down/pgdn", "scroll down"),
		),
		NewLine: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "new line"),
		),
		Help: key.NewBinding(
			key.WithKeys("ctrl+?"),
			key.WithHelp("ctrl+?", "help"),
		),
		Settings: key.NewBinding(
			key.WithKeys("ctrl+k"),
			key.WithHelp("ctrl+k", "settings"),
		),
	}
}
