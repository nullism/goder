package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typeChar simulates typing a single character into the input.
func typeChar(input *Input, ch rune) {
	input.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{ch},
	})
}

func TestInputSoftWrapGrows(t *testing.T) {
	// totalWidth is the outer width passed to View().
	// Input.Update calls textarea.SetWidth(totalWidth - 6).
	// SetWidth subtracts the prompt width (2 spaces) to get the wrapping width.
	// So effective wrapping width = totalWidth - 6 - 2 = totalWidth - 8.
	//
	// With totalWidth=26, wrapping width = 18.
	const totalWidth = 26

	input := NewInput()
	input.SetWidth(totalWidth)
	// Call View once to render with the correct width.
	input.View(totalWidth, PlanMode)

	wrapWidth := input.textArea.Width()
	if wrapWidth != 18 {
		t.Fatalf("Expected wrapping width 18, got %d", wrapWidth)
	}

	// Type characters past the wrap boundary and verify the textarea
	// height always accommodates the display line count.
	for i := 0; i < wrapWidth+5; i++ {
		ch := rune('a' + (i % 26))
		typeChar(&input, ch)
		input.View(totalWidth, PlanMode)

		if i+1 >= wrapWidth-1 {
			val := input.Value()
			dispLines := displayLineCount(val, input.textArea.Width())
			taHeight := input.textArea.Height()

			if taHeight < dispLines {
				step := fmt.Sprintf("char %d '%c'", i+1, ch)
				t.Errorf("[%s] textarea height %d < displayLineCount %d",
					step, taHeight, dispLines)
			}
		}
	}
}

func TestInputSoftWrapContentVisible(t *testing.T) {
	// Verify that when text wraps, the first line of content remains
	// visible in the rendered output (not scrolled away).
	const totalWidth = 26

	input := NewInput()
	input.SetWidth(totalWidth)
	input.View(totalWidth, PlanMode)

	wrapWidth := input.textArea.Width()

	// Type enough characters to cause wrapping.
	text := strings.Repeat("x", wrapWidth+5)
	for _, ch := range text {
		typeChar(&input, ch)
	}

	rendered := input.View(totalWidth, PlanMode)

	if !strings.Contains(rendered, "xxxxx") {
		t.Error("Rendered output does not contain the expected text 'xxxxx'")
	}

	if input.textArea.Height() < 2 {
		t.Errorf("Expected textarea height >= 2 after wrap, got %d", input.textArea.Height())
	}
}

func TestInputSoftWrapWithSpaces(t *testing.T) {
	// Test word-wrapping behaviour (text with spaces).
	const totalWidth = 26

	input := NewInput()
	input.SetWidth(totalWidth)
	input.View(totalWidth, PlanMode)

	text := "hello world this is a test of word wrapping"
	for _, ch := range text {
		typeChar(&input, ch)
	}

	rendered := input.View(totalWidth, PlanMode)

	if !strings.Contains(rendered, "hello") {
		t.Error("Rendered output missing 'hello' (start of text)")
	}
	if !strings.Contains(rendered, "wrapping") {
		t.Error("Rendered output missing 'wrapping' (end of text)")
	}
}

func TestDisplayLineCount(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		width    int
		expected int
	}{
		{"empty", "", 20, 1},
		{"short line", "hello", 20, 1},
		{"exact width minus 1", strings.Repeat("a", 19), 20, 1},
		{"exact width", strings.Repeat("a", 20), 20, 2}, // textarea wraps at >=
		{"one over width", strings.Repeat("a", 21), 20, 2},
		{"double width", strings.Repeat("a", 40), 20, 3}, // 40+1 / 20 = ceil(2.05) = 3
		{"two hard lines", "hello\nworld", 20, 2},
		{"hard line + wrap", "hello\n" + strings.Repeat("a", 25), 20, 3},
		{"zero width", "hello", 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayLineCount(tt.text, tt.width)
			if got != tt.expected {
				t.Errorf("displayLineCount(%q, %d) = %d, want %d",
					tt.text, tt.width, got, tt.expected)
			}
		})
	}
}
