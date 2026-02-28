package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// Mode represents the operating mode of the application.
type Mode int

const (
	// PlanMode is read-only analysis mode. The assistant will reason about the
	// codebase but will not make changes.
	PlanMode Mode = iota

	// BuildMode allows the assistant to create, edit, and delete files.
	BuildMode
)

func (m Mode) String() string {
	switch m {
	case PlanMode:
		return "plan"
	case BuildMode:
		return "build"
	default:
		return "unknown"
	}
}

// Model is the top-level bubbletea model for the application.
type Model struct {
	mode     Mode
	keys     KeyMap
	input    Input
	messages MessageList
	width    int
	height   int
	err      error
}

// New creates and returns a new Model with sensible defaults.
func New() Model {
	return Model{
		mode:     PlanMode,
		keys:     DefaultKeyMap(),
		input:    NewInput(),
		messages: NewMessageList(),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.SetWindowTitle("goder"),
		m.input.Focus(),
	)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch {

		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit

		case key.Matches(msg, m.keys.ToggleMode):
			if m.mode == PlanMode {
				m.mode = BuildMode
				m.messages.Add(SystemRole,
					fmt.Sprintf("Switched to %s mode. The assistant can now create and modify files.", m.mode))
			} else {
				m.mode = PlanMode
				m.messages.Add(SystemRole,
					fmt.Sprintf("Switched to %s mode. The assistant will only analyze, not modify files.", m.mode))
			}
			return m, nil

		case key.Matches(msg, m.keys.Submit):
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}

			m.messages.Add(UserRole, val)
			m.input.Reset()

			// Scaffold: simulate an assistant response based on mode
			switch m.mode {
			case PlanMode:
				m.messages.Add(AssistantRole,
					fmt.Sprintf("[plan] I'll analyze your request: %q\n(This is a scaffolded response. Integrate your LLM backend here.)", val))
			case BuildMode:
				m.messages.Add(AssistantRole,
					fmt.Sprintf("[build] I'll implement your request: %q\n(This is a scaffolded response. Integrate your LLM backend here.)", val))
			}
			return m, nil
		}

	case errMsg:
		m.err = msg
		return m, nil
	}

	// Forward remaining messages to the text input
	cmd := m.input.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "loading..."
	}

	// Layout: header (1 line) + messages (flexible) + input (dynamic) + status (1 line)
	headerHeight := 1
	inputHeight := m.input.Height()
	statusHeight := 1
	separatorLines := 2 // blank lines between sections
	msgHeight := m.height - headerHeight - inputHeight - statusHeight - separatorLines
	if msgHeight < 3 {
		msgHeight = 3
	}

	header := HeaderView(m.mode, m.width)
	msgs := m.messages.View(m.width, msgHeight)
	input := m.input.View(m.width)
	status := StatusBarView(m.mode, len(m.messages.messages), m.width)

	return fmt.Sprintf("%s\n%s\n%s\n%s", header, msgs, input, status)
}

// errMsg wraps an error for bubbletea.
type errMsg error
