package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Input wraps a bubbles textinput for the prompt area.
type Input struct {
	textInput textinput.Model
	focused   bool
}

// NewInput creates a new text input with the appropriate styling.
func NewInput() Input {
	ti := textinput.New()
	ti.Placeholder = "Ask anything..."
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 80
	ti.PromptStyle = inputPromptStyle
	ti.Prompt = "> "

	return Input{
		textInput: ti,
		focused:   true,
	}
}

// Update handles input events.
func (i *Input) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	i.textInput, cmd = i.textInput.Update(msg)
	return cmd
}

// View renders the input area.
func (i *Input) View(width int) string {
	i.textInput.Width = width - 6 // account for border + padding + prompt
	if i.textInput.Width < 10 {
		i.textInput.Width = 10
	}

	style := inputBorderStyle
	if i.focused {
		style = inputFocusedBorderStyle
	}

	return style.Width(width - 4).Render(i.textInput.View())
}

// Value returns the current text in the input.
func (i *Input) Value() string {
	return i.textInput.Value()
}

// Reset clears the input.
func (i *Input) Reset() {
	i.textInput.Reset()
}

// Focus gives focus to the input.
func (i *Input) Focus() tea.Cmd {
	i.focused = true
	return i.textInput.Focus()
}

// Blur removes focus from the input.
func (i *Input) Blur() {
	i.focused = false
	i.textInput.Blur()
}
