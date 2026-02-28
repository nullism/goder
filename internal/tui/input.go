package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	rw "github.com/mattn/go-runewidth"
)

const maxInputHeight = 6

// Input wraps a bubbles textarea for the prompt area.
type Input struct {
	textArea textarea.Model
	focused  bool
	width    int // total available width, set via SetWidth
}

// NewInput creates a new text area input with the appropriate styling.
func NewInput() Input {
	ta := textarea.New()
	ta.Placeholder = "Ask anything..."
	ta.Focus()
	ta.CharLimit = 4096
	ta.MaxHeight = maxInputHeight
	ta.ShowLineNumbers = false
	ta.Prompt = "  "
	ta.SetHeight(1)

	// Keep the default InsertNewline binding (enter) so the textarea
	// handles newlines naturally. Submit is bound to ctrl+s instead.

	// Disable TransposeCharacterBackward to avoid conflict with ctrl+t (toggle mode).
	ta.KeyMap.TransposeCharacterBackward = key.NewBinding(
		key.WithKeys(),
		key.WithDisabled(),
	)

	// Style the textarea to match the application theme.
	focused, blurred := textarea.DefaultStyles()

	focused.Prompt = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	focused.Text = lipgloss.NewStyle().Foreground(colorText)
	focused.Placeholder = lipgloss.NewStyle().Foreground(colorDim)
	focused.CursorLine = lipgloss.NewStyle()
	focused.EndOfBuffer = lipgloss.NewStyle().Foreground(colorDim)

	blurred.Prompt = lipgloss.NewStyle().Foreground(colorDim)
	blurred.Text = lipgloss.NewStyle().Foreground(colorText)
	blurred.Placeholder = lipgloss.NewStyle().Foreground(colorDim)

	ta.FocusedStyle = focused
	ta.BlurredStyle = blurred

	return Input{
		textArea: ta,
		focused:  true,
	}
}

// Update handles input events and auto-grows the textarea height.
func (i *Input) Update(msg tea.Msg) tea.Cmd {
	// Apply the stored width so the textarea wraps correctly.
	// This must happen here (not in View) because View is a value-receiver
	// on Model — any SetWidth call made there is lost when the copy is
	// discarded. Update's result is kept by the Bubble Tea event loop.
	if i.width > 0 {
		i.textArea.SetWidth(i.width - 6)
	}

	// Pre-expand the viewport to max height so that the textarea's internal
	// repositionView (called at the end of its Update) never needs to scroll
	// when new lines are added. Without this, pressing Enter causes the
	// viewport to scroll down with the old (smaller) height, hiding the
	// top lines until the user moves the cursor back up.
	i.textArea.SetHeight(maxInputHeight)

	var cmd tea.Cmd
	i.textArea, cmd = i.textArea.Update(msg)

	// Shrink back to fit the actual content, accounting for soft-wrapped lines.
	lines := displayLineCount(i.textArea.Value(), i.textArea.Width())

	if lines < 1 {
		lines = 1
	}
	if lines > maxInputHeight {
		lines = maxInputHeight
	}
	i.textArea.SetHeight(lines)

	return cmd
}

// SetWidth stores the total available width so that Update can apply it to
// the textarea before processing messages. This is necessary because
// Model.View() is a value receiver — any mutations it makes (including
// textarea.SetWidth) are discarded after rendering.
func (i *Input) SetWidth(width int) {
	i.width = width
}

// View renders the input area.
func (i *Input) View(width int, mode Mode) string {
	// Apply the width to the textarea for this render pass. Even though
	// this mutation is lost (View runs on a copy of Model), it ensures the
	// textarea.View() output has the correct width for this frame.
	i.textArea.SetWidth(width - 6)

	borderColor := colorPlan
	if mode == BuildMode {
		borderColor = colorBuild
	}

	style := inputBorderStyle.BorderForeground(borderColor)
	if i.focused {
		style = inputFocusedBorderStyle.BorderForeground(borderColor)
	}

	return style.Width(width - 4).Render(i.textArea.View())
}

// Value returns the current text in the input.
func (i *Input) Value() string {
	return i.textArea.Value()
}

// Reset clears the input and shrinks it back to a single line.
func (i *Input) Reset() {
	i.textArea.Reset()
	i.textArea.SetHeight(1)
}

// Focus gives focus to the input.
func (i *Input) Focus() tea.Cmd {
	i.focused = true
	return i.textArea.Focus()
}

// Blur removes focus from the input.
func (i *Input) Blur() {
	i.focused = false
	i.textArea.Blur()
}

// Height returns the current rendered height of the input area including borders.
func (i *Input) Height() int {
	// textarea height + 2 for top/bottom border
	return i.textArea.Height() + 2
}

// displayLineCount returns the total number of display rows the text occupies,
// accounting for soft-wrapped lines. Each logical line (separated by \n) takes
// at least 1 row, and long lines take ceil(displayWidth / wrapWidth) rows.
func displayLineCount(text string, width int) int {
	if width <= 0 {
		// Can't compute wrapping without a valid width; fall back to
		// counting hard lines only.
		return max(1, strings.Count(text, "\n")+1)
	}
	total := 0
	for _, line := range strings.Split(text, "\n") {
		w := rw.StringWidth(line)
		if w < width {
			total++
		} else {
			// Add 1 to account for the trailing space that the textarea's
			// internal wrap() appends to each soft-wrapped line segment.
			total += int(math.Ceil(float64(w+1) / float64(width)))
		}
	}
	if total < 1 {
		total = 1
	}
	return total
}
