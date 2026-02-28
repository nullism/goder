package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/webgovernor/goder/internal/config"
	"github.com/webgovernor/goder/internal/db"
	"github.com/webgovernor/goder/internal/llm/agent"
	"github.com/webgovernor/goder/internal/llm/provider"
	"github.com/webgovernor/goder/internal/message"
	"github.com/webgovernor/goder/internal/permission"
	"github.com/webgovernor/goder/internal/session"
	"github.com/webgovernor/goder/internal/tools"
)

// programRef holds a shared reference to the tea.Program.
// Because Bubble Tea copies the Model value, a plain *tea.Program field
// on the Model would be nil inside the running copy.  By storing the
// pointer behind an atomic.Pointer inside a heap-allocated struct, every
// copy of Model shares the same reference.
type programRef struct {
	p atomic.Pointer[tea.Program]
}

func (r *programRef) Store(p *tea.Program) { r.p.Store(p) }
func (r *programRef) Load() *tea.Program   { return r.p.Load() }

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
	// Core state
	mode   Mode
	keys   KeyMap
	input  Input
	msgs   MessageList
	width  int
	height int
	err    error

	// Services
	cfg      config.Config
	database *db.DB
	sessions *session.Service
	registry *tools.Registry
	prov     provider.Provider
	permSvc  *permission.Service

	// Agent state
	agentCancel context.CancelFunc
	thinking    bool                // true while agent is processing
	streamBuf   string              // accumulates streaming text (plain string to avoid strings.Builder copy panic)
	permReq     *permission.Request // pending permission request

	// Program reference for sending commands from goroutines.
	// This is a pointer to a shared struct so that all copies of Model
	// (including the one inside tea.Program) share the same reference.
	progRef *programRef
}

// New creates and returns a new Model.
func New(cfg config.Config, database *db.DB, sessions *session.Service, registry *tools.Registry, prov provider.Provider, permSvc *permission.Service) Model {
	return Model{
		mode:     PlanMode,
		keys:     DefaultKeyMap(),
		input:    NewInput(),
		msgs:     NewMessageList(),
		cfg:      cfg,
		database: database,
		sessions: sessions,
		registry: registry,
		prov:     prov,
		permSvc:  permSvc,
		progRef:  &programRef{}, // shared across Bubble Tea value copies
	}
}

// SetProgram stores a reference to the tea.Program for async command sending.
// Safe to call after tea.NewProgram because progRef is shared across copies.
func (m *Model) SetProgram(p *tea.Program) {
	m.progRef.Store(p)
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.SetWindowTitle("goder"),
		m.input.Focus(),
		m.initSession(),
		m.listenForPermissions(),
	)
}

// initSession creates or loads the initial session.
func (m Model) initSession() tea.Cmd {
	return func() tea.Msg {
		sess, err := m.sessions.Current()
		if err != nil {
			return errMsg(fmt.Errorf("initializing session: %w", err))
		}
		return sessionLoadedMsg{session: sess}
	}
}

// listenForPermissions starts listening for the next permission request.
func (m Model) listenForPermissions() tea.Cmd {
	permCh := m.permSvc.RequestCh()
	return func() tea.Msg {
		req, ok := <-permCh
		if !ok {
			return nil
		}
		return permissionRequestMsg{request: req}
	}
}

// --- Message types for async operations ---

type sessionLoadedMsg struct{ session *db.Session }
type errMsg error

// agentEventMsg wraps an agent event for the TUI.
type agentEventMsg struct{ event agent.Event }

// permissionRequestMsg wraps a permission request for the TUI.
type permissionRequestMsg struct{ request permission.Request }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sessionLoadedMsg:
		// Load messages from the session
		messages, err := m.sessions.GetMessages()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.msgs.LoadFromMessages(messages)
		return m, nil

	case permissionRequestMsg:
		m.permReq = &msg.request
		return m, nil

	case agentEventMsg:
		return m.handleAgentEvent(msg.event)

	case tea.KeyMsg:
		// Handle permission dialog keys first
		if m.permReq != nil {
			return m.handlePermissionKey(msg)
		}

		switch {
		case key.Matches(msg, m.keys.Quit):
			if m.agentCancel != nil {
				m.agentCancel()
			}
			return m, tea.Quit

		case key.Matches(msg, m.keys.Cancel):
			if m.thinking && m.agentCancel != nil {
				m.agentCancel()
				m.agentCancel = nil
				m.thinking = false
				m.msgs.Add(message.System, "Agent cancelled.")
				return m, m.listenForPermissions()
			}

		case key.Matches(msg, m.keys.ToggleMode):
			if m.thinking {
				return m, nil // don't toggle while agent is running
			}
			if m.mode == PlanMode {
				m.mode = BuildMode
				m.msgs.Add(message.System,
					"Switched to BUILD mode. The assistant can now create and modify files.")
			} else {
				m.mode = PlanMode
				m.msgs.Add(message.System,
					"Switched to PLAN mode. The assistant will only analyze, not modify files.")
			}
			return m, nil

		case key.Matches(msg, m.keys.Submit):
			if m.thinking {
				return m, nil // don't submit while agent is running
			}
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}

			m.input.Reset()
			return m, m.submitPrompt(val)
		}

	case errMsg:
		m.err = msg
		return m, nil
	}

	// Forward remaining messages to the text input (only if not thinking)
	if !m.thinking {
		cmd := m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// submitPrompt sends a user message and starts the agent loop.
func (m *Model) submitPrompt(prompt string) tea.Cmd {
	// Add user message
	sessionID := m.sessions.CurrentID()
	userMsg := message.NewUserMessage(sessionID, prompt)
	m.msgs.AddMessage(userMsg)
	m.thinking = true
	m.streamBuf = ""

	// Persist user message
	if err := m.sessions.AddMessage(userMsg); err != nil {
		m.thinking = false
		m.err = err
		return func() tea.Msg {
			return errMsg(fmt.Errorf("persisting user message: %w", err))
		}
	}

	// Get conversation history
	history, err := m.sessions.GetMessages()
	if err != nil {
		return func() tea.Msg {
			return errMsg(fmt.Errorf("loading history: %w", err))
		}
	}

	// Create agent
	ctx, cancel := context.WithCancel(context.Background())
	m.agentCancel = cancel

	ag := agent.New(agent.Config{
		Provider:  m.prov,
		Registry:  m.registry,
		PermSvc:   m.permSvc,
		WorkDir:   m.cfg.WorkDir,
		Mode:      m.mode.String(),
		MaxTokens: m.cfg.MaxTokens,
	})

	program := m.progRef.Load()

	// Return a command that reads from the agent event channel
	return func() tea.Msg {
		eventCh := ag.Run(ctx, history, sessionID)
		event, ok := <-eventCh
		if !ok {
			return agentEventMsg{event: agent.Event{Type: agent.EventAgentDone}}
		}

		// Start a goroutine to forward remaining events
		go func() {
			for ev := range eventCh {
				if program != nil {
					program.Send(agentEventMsg{event: ev})
				}
			}
		}()

		return agentEventMsg{event: event}
	}
}

// handleAgentEvent processes events from the agent loop.
func (m Model) handleAgentEvent(event agent.Event) (tea.Model, tea.Cmd) {
	switch event.Type {
	case agent.EventStreamText:
		m.streamBuf += event.Text
		// Update the streaming message in the list
		m.msgs.UpdateStreaming(m.streamBuf)
		return m, nil

	case agent.EventToolCallStart:
		m.msgs.AddToolCall(event.ToolCallName, event.ToolInput)
		return m, nil

	case agent.EventToolCallEnd:
		m.msgs.UpdateLastToolCall(event.ToolCallName, event.ToolInput)
		return m, nil

	case agent.EventToolResult:
		m.msgs.AddToolResult(event.ToolCallName, event.ToolOutput, event.ToolIsError)
		return m, nil

	case agent.EventPersistMessage:
		// Persist intermediate messages (assistant with tool calls, tool results)
		if event.FinalMessage != nil {
			if err := m.sessions.AddMessage(*event.FinalMessage); err != nil {
				m.err = err
			}
			// Also reset the stream buffer since the assistant turn is complete
			// and a new LLM call will start after tool results.
			m.msgs.FinalizeStreaming(event.FinalMessage.Content)
			m.streamBuf = ""
		}
		return m, nil

	case agent.EventAgentDone:
		m.thinking = false
		if event.FinalMessage != nil {
			// Persist the assistant message
			if err := m.sessions.AddMessage(*event.FinalMessage); err != nil {
				m.err = err
			}
			// Finalize the streaming message
			m.msgs.FinalizeStreaming(event.FinalMessage.Content)
		}
		m.streamBuf = ""
		return m, m.listenForPermissions()

	case agent.EventAgentError:
		m.thinking = false
		m.streamBuf = ""
		errText := "Agent error"
		if event.Error != nil {
			errText = fmt.Sprintf("Error: %s", event.Error.Error())
		}
		m.msgs.Add(message.System, errText)
		return m, m.listenForPermissions()
	}

	return m, nil
}

// handlePermissionKey handles key presses in the permission dialog.
func (m Model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.permReq.ResponseCh <- permission.Allow
		m.permReq = nil
		return m, m.listenForPermissions()
	case "n", "N":
		m.permReq.ResponseCh <- permission.Deny
		m.permReq = nil
		return m, m.listenForPermissions()
	case "a", "A":
		m.permReq.ResponseCh <- permission.AllowForSession
		m.permReq = nil
		return m, m.listenForPermissions()
	}
	return m, nil
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

	header := HeaderView(m.mode, m.cfg.Model, m.width)
	msgs := m.msgs.View(m.width, msgHeight)

	// Show permission dialog overlay if needed
	var inputView string
	if m.permReq != nil {
		inputView = m.renderPermissionDialog()
	} else if m.thinking {
		inputView = thinkingStyle.Width(m.width - 4).Render("  thinking...")
	} else {
		inputView = m.input.View(m.width)
	}

	status := StatusBarView(m.mode, m.msgs.Count(), m.width, m.thinking, m.cfg.Model)

	return fmt.Sprintf("%s\n%s\n%s\n%s", header, msgs, inputView, status)
}

// renderPermissionDialog renders the permission approval dialog.
func (m Model) renderPermissionDialog() string {
	if m.permReq == nil {
		return ""
	}

	toolName := m.permReq.ToolName
	input := m.permReq.Input
	if len(input) > 200 {
		input = input[:200] + "..."
	}

	dialog := fmt.Sprintf(
		"  Tool: %s\n  Input: %s\n\n  [y] Allow  [n] Deny  [a] Allow for session",
		toolName, input,
	)

	return permissionStyle.Width(m.width - 4).Render(dialog)
}
