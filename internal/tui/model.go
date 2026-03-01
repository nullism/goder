package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/webgovernor/goder/internal/auth"
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

	// Session usage state
	tokenTotal int

	// Agent state
	agentCancel context.CancelFunc
	thinking    bool                // true while agent is processing
	streamBuf   string              // accumulates streaming text (plain string to avoid strings.Builder copy panic)
	permReq     *permission.Request // pending permission request

	// Settings overlay
	settings     Settings
	settingsOpen bool

	// Quit confirmation
	confirmQuit bool

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
		settings: NewSettings(provider.Supported(), cfg.Provider),
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
	cmds := []tea.Cmd{
		tea.SetWindowTitle("goder"),
		m.input.Focus(),
		m.initSession(),
		m.listenForPermissions(),
	}

	// If no API key is configured, show a helpful message
	if m.cfg.APIKeyFor(m.cfg.Provider) == "" {
		m.msgs.Add(message.System,
			fmt.Sprintf("No API key configured for provider %q. Press ctrl+k to open settings.", m.cfg.Provider))
	}

	return tea.Batch(cmds...)
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
		m.input.SetWidth(msg.Width)
		return m, nil

	case sessionLoadedMsg:
		// Load messages from the session
		messages, err := m.sessions.GetMessages()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.msgs.LoadFromMessages(messages)
		total, err := m.sessions.GetTokenTotal()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.tokenTotal = total
		return m, nil

	case permissionRequestMsg:
		m.permReq = &msg.request
		return m, nil

	case agentEventMsg:
		return m.handleAgentEvent(msg.event)

	case modelsLoadedMsg:
		m.settings.HandleModelsLoaded(msg.models, msg.err)
		return m, nil

	case copilotDeviceCodeMsg:
		m.settings.HandleCopilotDeviceCode(msg.userCode, msg.url, msg.err)
		if msg.err != nil {
			return m, nil
		}
		// Start polling for the token in the background.
		// The tea.Cmd runs in its own goroutine, so PollForToken can block
		// until the user completes the flow (or the context is cancelled).
		ctx, cancel := context.WithCancel(context.Background())
		m.settings.SetCopilotCancel(cancel)
		deviceCode := msg.deviceCode
		interval := msg.interval
		return m, func() tea.Msg {
			token, err := auth.PollForToken(ctx, deviceCode, interval)
			return copilotAuthMsg{token: token, err: err}
		}

	case copilotAuthMsg:
		m.settings.HandleCopilotAuth(msg.token, msg.err)
		if msg.err != nil {
			return m, nil
		}
		// Save the token and switch to copilot provider.
		m.cfg.SetAPIKeyFor("copilot", msg.token)
		m.cfg.Provider = "copilot"
		m.cfg.Model = "gpt-4o" // default copilot model

		// Create a new copilot provider instance.
		newProv, err := provider.New("copilot", msg.token, m.cfg.Model)
		if err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Provider error: %s", err.Error()), true)
			return m, nil
		}
		m.prov = newProv

		// Persist to config file.
		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, nil
		}

		m.settings.SetFeedback("Copilot authenticated successfully", false)
		m.settings.SetView(settingsViewProviderMenu)
		return m, nil

	case tea.KeyMsg:
		if m.confirmQuit {
			return m.handleQuitConfirmKey(msg)
		}

		// Handle settings overlay if open
		if m.settingsOpen {
			return m.handleSettingsKey(msg)
		}

		// Handle permission dialog keys first
		if m.permReq != nil {
			return m.handlePermissionKey(msg)
		}

		scrollAmount := m.messageScrollAmount()

		switch {
		case key.Matches(msg, m.keys.ScrollUp):
			m.msgs.ScrollUp(scrollAmount)
			return m, nil

		case key.Matches(msg, m.keys.ScrollDown):
			m.msgs.ScrollDown(scrollAmount)
			return m, nil

		case key.Matches(msg, m.keys.Quit):
			m.confirmQuit = true
			return m, nil

		case key.Matches(msg, m.keys.Cancel):
			if m.thinking && m.agentCancel != nil {
				m.agentCancel()
				m.agentCancel = nil
				m.thinking = false
				m.msgs.Add(message.System, "Agent cancelled.")
				return m, m.listenForPermissions()
			}

		case key.Matches(msg, m.keys.Settings):
			if !m.thinking {
				m.settingsOpen = true
				m.settings = NewSettings(provider.Supported(), m.cfg.Provider) // reset state
				m.input.Blur()
				return m, nil
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
	// Check if API key is configured
	if m.cfg.APIKeyFor(m.cfg.Provider) == "" {
		m.msgs.Add(message.System,
			fmt.Sprintf("No API key configured for provider %q. Press ctrl+k to open settings.", m.cfg.Provider))
		return nil
	}

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
		Provider:      m.prov,
		Registry:      m.registry,
		PermSvc:       m.permSvc,
		WorkDir:       m.cfg.WorkDir,
		Mode:          m.mode.String(),
		Model:         m.cfg.Model,
		MaxTokens:     m.cfg.MaxTokens,
		MaxIterations: m.cfg.MaxIterations,
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
			m.tokenTotal += event.FinalMessage.TotalTokens
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
			m.tokenTotal += event.FinalMessage.TotalTokens
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

// handleSettingsKey routes key events to the settings overlay and handles
// the resulting actions (save API key, select model, close overlay).
func (m Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prevView := m.settings.view

	settings, shouldClose, cmd := m.settings.Update(msg)
	m.settings = settings

	if shouldClose {
		m.settingsOpen = false
		return m, m.input.Focus()
	}

	// Handle transition to model selection (trigger fetch)
	if prevView != settingsViewModels && m.settings.view == settingsViewModels {
		selectedProvider := m.settings.SelectedProvider()
		// If browsing a different provider than the active one, create a
		// temporary provider instance for listing models.
		if selectedProvider != "" && selectedProvider != m.prov.Name() {
			apiKey := m.cfg.APIKeyFor(selectedProvider)
			tmpProv, err := provider.New(selectedProvider, apiKey, "")
			if err != nil {
				m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
				return m, nil
			}
			return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
		}
		if m.prov != nil {
			return m, fetchModelsCmd(context.Background(), m.prov.ListModels)
		}
		m.settings.HandleModelsLoaded(nil, fmt.Errorf("no provider configured (set API key first)"))
		return m, nil
	}

	// Handle transition to Copilot auth (trigger device code request)
	if prevView != settingsViewCopilotAuth && m.settings.view == settingsViewCopilotAuth {
		return m, func() tea.Msg {
			dcResp, err := auth.RequestDeviceCode(context.Background())
			if err != nil {
				return copilotDeviceCodeMsg{err: err}
			}
			return copilotDeviceCodeMsg{
				userCode:   dcResp.UserCode,
				url:        dcResp.VerificationURI,
				deviceCode: dcResp.DeviceCode,
				interval:   dcResp.Interval,
			}
		}
	}

	// Handle API key save on enter in API key view
	if m.settings.view == settingsViewAPIKey && msg.String() == "enter" {
		apiKey := m.settings.APIKeyValue()
		if apiKey == "" {
			return m, cmd
		}

		selectedProvider := m.settings.SelectedProvider()
		if selectedProvider == "" {
			selectedProvider = m.cfg.Provider
		}

		// Update config and active provider if applicable
		m.cfg.SetAPIKeyFor(selectedProvider, apiKey)
		if selectedProvider == m.cfg.Provider {
			m.prov.SetAPIKey(apiKey)
		}

		// Persist to config file
		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback("API key saved successfully", false)
		m.settings.SetView(settingsViewProviderMenu)
		return m, cmd
	}

	// Handle model selection on enter in model view
	if m.settings.view == settingsViewModels && msg.String() == "enter" {
		selected := m.settings.SelectedModel()
		if selected == "" {
			return m, cmd
		}

		// If the user is selecting a model for a different provider,
		// switch the active provider.
		selectedProvider := m.settings.SelectedProvider()
		if selectedProvider != "" && selectedProvider != m.cfg.Provider {
			apiKey := m.cfg.APIKeyFor(selectedProvider)
			newProv, err := provider.New(selectedProvider, apiKey, selected)
			if err != nil {
				m.settings.SetFeedback(fmt.Sprintf("Provider error: %s", err.Error()), true)
				return m, cmd
			}
			m.prov = newProv
			m.cfg.Provider = selectedProvider
		}

		// Update config and provider
		m.cfg.Model = selected
		m.prov.SetModel(selected)

		// Persist to config file
		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Model set to %s", selected), false)
		m.settings.view = settingsViewMenu
		return m, cmd
	}

	// Handle max iterations save on enter in max iterations view
	if m.settings.view == settingsViewMaxIter && msg.String() == "enter" {
		val := m.settings.MaxIterValue()
		if val == 0 {
			return m, cmd
		}

		// Update config
		m.cfg.MaxIterations = val

		// Persist to config file
		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Max iterations set to %d", val), false)
		m.settings.view = settingsViewMenu
		return m, cmd
	}

	return m, cmd
}

// messageScrollAmount returns the number of lines to scroll for each scroll action.
func (m Model) messageScrollAmount() int {
	if m.width == 0 {
		return 1
	}

	headerHeight := 1
	inputHeight := m.input.Height()
	statusHeight := 1
	separatorLines := 2
	msgHeight := m.height - headerHeight - inputHeight - statusHeight - separatorLines
	if msgHeight < 3 {
		msgHeight = 3
	}

	scroll := msgHeight / 2
	if scroll < 1 {
		scroll = 1
	}

	return scroll
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

	header := HeaderView(m.mode, m.cfg.Model, m.tokenTotal, m.width)
	msgs := m.msgs.View(m.width, msgHeight)

	// Show confirmation dialog if quitting
	var inputView string
	if m.confirmQuit {
		inputView = m.renderQuitConfirmDialog()
	} else if m.settingsOpen {
		inputView = m.settings.View(m.width, m.cfg.APIKeyFor(m.cfg.Provider), m.cfg.Model, m.cfg.MaxIterations)
	} else if m.permReq != nil {
		inputView = m.renderPermissionDialog()
	} else if m.thinking {
		inputView = thinkingStyle.Width(m.width - 4).Render("  thinking...")
	} else {
		inputView = m.input.View(m.width, m.mode)
	}

	status := StatusBarView(m.width, m.thinking)

	return fmt.Sprintf("%s\n%s\n%s\n%s", header, msgs, inputView, status)
}

// handleQuitConfirmKey handles key presses in the quit confirmation dialog.
func (m Model) handleQuitConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		if m.agentCancel != nil {
			m.agentCancel()
		}
		return m, tea.Quit
	case "n", "N", "esc":
		m.confirmQuit = false
		return m, nil
	}

	return m, nil
}

// renderQuitConfirmDialog renders the quit confirmation dialog.
func (m Model) renderQuitConfirmDialog() string {
	dialog := "  Quit goder?\n\n  [y] Yes  [n] No"
	return permissionStyle.Width(m.width - 4).Render(dialog)
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
