package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nullism/goder/internal/auth"
	"github.com/nullism/goder/internal/config"
	"github.com/nullism/goder/internal/db"
	"github.com/nullism/goder/internal/llm/agent"
	"github.com/nullism/goder/internal/llm/planner"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/permission"
	"github.com/nullism/goder/internal/session"
	"github.com/nullism/goder/internal/tools"
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

// Model is the top-level bubbletea model for the application.
type Model struct {
	// Core state
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

	// Planning agents
	plannerSpecs []planner.PlannerSpec

	// Session usage state
	tokenTotal        int
	tokenTotalByModel map[string]int

	// Agent state
	agentCancel     context.CancelFunc
	thinking        bool                // true while agent is processing
	streamBuf       string              // accumulates streaming text (plain string to avoid strings.Builder copy panic)
	permReq         *permission.Request // pending permission request
	plannerActive   bool                // true while a planner flow is in progress
	planSynthesized bool                // true after planners finished; next user msg goes to main agent

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
func New(cfg config.Config, database *db.DB, sessions *session.Service, registry *tools.Registry, prov provider.Provider, permSvc *permission.Service, plannerSpecs []planner.PlannerSpec) Model {
	return Model{
		keys:         DefaultKeyMap(),
		input:        NewInput(),
		msgs:         NewMessageList(),
		settings:     NewSettings(provider.Supported(), cfg.MainAgentProvider()),
		cfg:          cfg,
		database:     database,
		sessions:     sessions,
		registry:     registry,
		prov:         prov,
		permSvc:      permSvc,
		plannerSpecs: plannerSpecs,
		progRef:      &programRef{}, // shared across Bubble Tea value copies
	}
}

// SetProgram stores a reference to the tea.Program for async command sending.
// Safe to call after tea.NewProgram because progRef is shared across copies.
func (m *Model) SetProgram(p *tea.Program) {
	m.progRef.Store(p)
}

// rebuildPlannerSpecs recreates the planner specs from the current config.
// Called when planners are added or removed via the settings UI.
func rebuildPlannerSpecs(cfg config.Config) []planner.PlannerSpec {
	var specs []planner.PlannerSpec
	for _, pa := range cfg.Agents.Planners {
		planProv, err := provider.New(pa.Provider, cfg.APIKeyFor(pa.Provider), pa.Model)
		if err != nil {
			continue
		}
		specs = append(specs, planner.PlannerSpec{
			Provider: planProv,
			Model:    pa.Model,
		})
	}
	return specs
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.SetWindowTitle("goder"),
		m.input.Focus(),
		m.initSession(),
		m.listenForPermissions(),
	}

	// If no API key is configured for the main agent provider, show a helpful message.
	mainProvider := m.cfg.MainAgentProvider()
	if m.cfg.APIKeyFor(mainProvider) == "" {
		m.msgs.Add(message.System,
			fmt.Sprintf("No API key configured for provider %q. Press ctrl+k to open settings.", mainProvider))
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
		byModel, err := m.sessions.GetTokenTotalsByModel()
		if err != nil {
			m.err = err
			return m, nil
		}
		m.tokenTotalByModel = byModel
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
		// Save the token for Copilot. Provider settings are connection-only,
		// so this does not change the selected main agent provider/model.
		m.cfg.SetAPIKeyFor("copilot", msg.token)
		if m.prov != nil && m.prov.Name() == "copilot" {
			m.prov.SetAPIKey(msg.token)
		}

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
				m.settings = NewSettings(provider.Supported(), m.cfg.MainAgentProvider()) // reset state
				m.input.Blur()
				return m, nil
			}

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
	// Check if API key is configured for the main agent provider.
	mainProvider := m.cfg.MainAgentProvider()
	if m.cfg.APIKeyFor(mainProvider) == "" {
		m.msgs.Add(message.System,
			fmt.Sprintf("No API key configured for provider %q. Press ctrl+k to open settings.", mainProvider))
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

	// Choose between planner flow and single-agent loop.
	// After planners have synthesized a plan, the next user message
	// (e.g. "go ahead") should go to the main agent for execution,
	// not back to the planners.
	var eventCh <-chan agent.Event

	if len(m.plannerSpecs) > 0 && !m.planSynthesized {
		m.plannerActive = true
		pl := planner.New(planner.Config{
			MainProvider:  m.prov,
			PlannerSpecs:  m.plannerSpecs,
			Registry:      m.registry,
			PermSvc:       m.permSvc,
			WorkDir:       m.cfg.WorkDir,
			MainModel:     m.cfg.MainAgentModel(),
			MaxTokens:     m.cfg.MaxTokens,
			MaxIterations: m.cfg.MaxIterations,
		})
		eventCh = pl.Run(ctx, history, sessionID)
	} else {
		m.plannerActive = false
		ag := agent.New(agent.Config{
			Provider:      m.prov,
			Registry:      m.registry,
			PermSvc:       m.permSvc,
			WorkDir:       m.cfg.WorkDir,
			Model:         m.cfg.MainAgentModel(),
			MaxTokens:     m.cfg.MaxTokens,
			MaxIterations: m.cfg.MaxIterations,
		})
		eventCh = ag.Run(ctx, history, sessionID)
	}

	program := m.progRef.Load()

	// Return a command that reads from the agent event channel
	return func() tea.Msg {
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
			if event.FinalMessage.Model != "" {
				if m.tokenTotalByModel == nil {
					m.tokenTotalByModel = make(map[string]int)
				}
				m.tokenTotalByModel[event.FinalMessage.Model] += event.FinalMessage.TotalTokens
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
			m.tokenTotal += event.FinalMessage.TotalTokens
			if event.FinalMessage.Model != "" {
				if m.tokenTotalByModel == nil {
					m.tokenTotalByModel = make(map[string]int)
				}
				m.tokenTotalByModel[event.FinalMessage.Model] += event.FinalMessage.TotalTokens
			}
			// Finalize the streaming message
			m.msgs.FinalizeStreaming(event.FinalMessage.Content)
		}
		m.streamBuf = ""

		// Track planning state transitions:
		// - If the planner flow just finished, mark that the plan has been
		//   synthesized so the next user message goes to the main agent.
		// - If the main agent just finished executing (after a prior plan),
		//   reset so the next fresh task dispatches to planners again.
		if m.plannerActive {
			m.plannerActive = false
			m.planSynthesized = true
		} else {
			m.planSynthesized = false
		}

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

	case agent.EventPlanningPhase:
		m.msgs.AddPlanPhase(event.PlanPhase)
		return m, nil

	case agent.EventPlannerStart:
		m.msgs.AddPlannerStart(event.PlannerModel)
		return m, nil

	case agent.EventPlannerDone:
		m.msgs.AddPlannerDone(event.PlannerModel, event.PlannerPlan, event.PlannerError)
		if event.PlannerTokens > 0 && event.PlannerModel != "" {
			m.tokenTotal += event.PlannerTokens
			if m.tokenTotalByModel == nil {
				m.tokenTotalByModel = make(map[string]int)
			}
			m.tokenTotalByModel[event.PlannerModel] += event.PlannerTokens
		}
		return m, nil
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

	// Handle transition to main agent model selection (trigger fetch)
	if prevView != settingsViewAgentMainModels && m.settings.view == settingsViewAgentMainModels {
		provName := m.settings.AgentProviderPick()
		apiKey := m.cfg.APIKeyFor(provName)
		tmpProv, err := provider.New(provName, apiKey, "")
		if err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
			return m, nil
		}
		return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
	}

	// Handle transition to planner model selection (trigger fetch)
	if prevView != settingsViewPlannerModels && m.settings.view == settingsViewPlannerModels {
		provName := m.settings.AgentProviderPick()
		apiKey := m.cfg.APIKeyFor(provName)
		tmpProv, err := provider.New(provName, apiKey, "")
		if err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
			return m, nil
		}
		return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
	}

	// Handle transition to planners list — sync local copy from config
	if prevView != settingsViewPlanners && m.settings.view == settingsViewPlanners {
		var entries []agentEntry
		for _, pa := range m.cfg.Agents.Planners {
			entries = append(entries, agentEntry{Provider: pa.Provider, Model: pa.Model})
		}
		m.settings.SetPlanners(entries)
	}

	// Handle API key save on enter in API key view
	if m.settings.view == settingsViewAPIKey && msg.String() == "enter" {
		apiKey := m.settings.APIKeyValue()
		if apiKey == "" {
			return m, cmd
		}

		selectedProvider := m.settings.SelectedProvider()
		if selectedProvider == "" {
			selectedProvider = m.cfg.MainAgentProvider()
		}

		// Update config and active provider if applicable
		m.cfg.SetAPIKeyFor(selectedProvider, apiKey)
		if m.prov != nil && selectedProvider == m.prov.Name() {
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

	// Handle main agent model selection on enter
	if m.settings.view == settingsViewAgentMainModels && msg.String() == "enter" {
		selected := m.settings.SelectedModel()
		if selected == "" {
			return m, cmd
		}

		provName := m.settings.AgentProviderPick()
		if m.cfg.Agents.Main == nil {
			m.cfg.Agents.Main = &config.AgentSpec{}
		}
		m.cfg.Agents.Main.Provider = provName
		m.cfg.Agents.Main.Model = selected

		// Rebuild the main provider if the main agent changed
		apiKey := m.cfg.APIKeyFor(provName)
		newProv, err := provider.New(provName, apiKey, selected)
		if err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Provider error: %s", err.Error()), true)
			return m, cmd
		}
		m.prov = newProv

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Main agent set to %s:%s", provName, selected), false)
		m.settings.view = settingsViewAgents
		return m, cmd
	}

	// Handle planner model selection on enter (adding a new planner)
	if m.settings.view == settingsViewPlannerModels && msg.String() == "enter" {
		selected := m.settings.SelectedModel()
		if selected == "" {
			return m, cmd
		}

		provName := m.settings.AgentProviderPick()
		newSpec := config.AgentSpec{Provider: provName, Model: selected}
		m.cfg.Agents.Planners = append(m.cfg.Agents.Planners, newSpec)

		// Rebuild planner specs
		m.plannerSpecs = rebuildPlannerSpecs(m.cfg)

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Added planner %s:%s", provName, selected), false)
		m.settings.view = settingsViewPlanners
		// Re-sync planners list
		var entries []agentEntry
		for _, pa := range m.cfg.Agents.Planners {
			entries = append(entries, agentEntry{Provider: pa.Provider, Model: pa.Model})
		}
		m.settings.SetPlanners(entries)
		return m, cmd
	}

	// Handle planner deletion — detect when the local planners list changed
	if m.settings.view == settingsViewPlanners && msg.String() == "d" {
		localPlanners := m.settings.Planners()
		if len(localPlanners) != len(m.cfg.Agents.Planners) {
			// Sync config from local copy
			m.cfg.Agents.Planners = make([]config.AgentSpec, len(localPlanners))
			for i, p := range localPlanners {
				m.cfg.Agents.Planners[i] = config.AgentSpec{Provider: p.Provider, Model: p.Model}
			}

			// Rebuild planner specs
			m.plannerSpecs = rebuildPlannerSpecs(m.cfg)

			if err := config.Save(m.cfg); err != nil {
				m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
				return m, cmd
			}
			m.settings.SetFeedback("Planner removed", false)
			return m, cmd
		}
	}

	return m, cmd
}

// headerHeight returns the rendered height of the header, accounting for the
// optional per-model token breakdown line.
func (m Model) headerHeight() int {
	if len(m.tokenTotalByModel) >= 2 {
		return 2
	}
	return 1
}

// messageScrollAmount returns the number of lines to scroll for each scroll action.
func (m Model) messageScrollAmount() int {
	if m.width == 0 {
		return 1
	}

	headerHeight := m.headerHeight()
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

	// Layout: header (dynamic) + messages (flexible) + input (dynamic) + status (1 line)
	header := HeaderView(m.cfg.MainAgentModel(), m.tokenTotal, m.tokenTotalByModel, m.width)
	headerHeight := lipgloss.Height(header)
	inputHeight := m.input.Height()
	statusHeight := 1
	separatorLines := 2 // blank lines between sections
	msgHeight := m.height - headerHeight - inputHeight - statusHeight - separatorLines
	if msgHeight < 3 {
		msgHeight = 3
	}

	msgs := m.msgs.View(m.width, msgHeight)

	// Show confirmation dialog if quitting
	var inputView string
	if m.confirmQuit {
		inputView = m.renderQuitConfirmDialog()
	} else if m.settingsOpen {
		mainAgent := agentEntry{Provider: m.cfg.MainAgentProvider(), Model: m.cfg.MainAgentModel()}
		var plannerEntries []agentEntry
		for _, pa := range m.cfg.Agents.Planners {
			plannerEntries = append(plannerEntries, agentEntry{Provider: pa.Provider, Model: pa.Model})
		}
		selectedProvider := m.settings.SelectedProvider()
		if selectedProvider == "" {
			selectedProvider = m.cfg.MainAgentProvider()
		}
		inputView = m.settings.View(m.width, m.cfg.APIKeyFor(selectedProvider), m.cfg.MaxIterations, mainAgent, plannerEntries)
	} else if m.permReq != nil {
		inputView = m.renderPermissionDialog()
	} else if m.thinking {
		inputView = thinkingStyle.Width(m.width - 4).Render("  thinking...")
	} else {
		inputView = m.input.View(m.width)
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
