package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nullism/goder/internal/auth"
	"github.com/nullism/goder/internal/config"
	"github.com/nullism/goder/internal/db"
	"github.com/nullism/goder/internal/llm/agent"
	"github.com/nullism/goder/internal/llm/planner"
	"github.com/nullism/goder/internal/llm/prompt"
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

type runRole int

const (
	runRoleNone runRole = iota
	runRoleMain
	runRolePlanner
	runRoleProgrammer
)

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
	mainProv provider.Provider
	progProv provider.Provider
	permSvc  *permission.Service

	// Review agent
	reviewerSpec *planner.ReviewerSpec

	// Session usage state
	tokenTotal        int
	tokenTotalByModel map[string]int

	// Agent state
	agentCancel     context.CancelFunc
	thinking        bool                // true while agent is processing
	activeRunRole   runRole             // current executing role
	streamBuf       string              // accumulates streaming text (plain string to avoid strings.Builder copy panic)
	permReq         *permission.Request // pending permission request
	pendingPlan     string              // latest reviewed plan awaiting user approval
	planAwaitingAck bool                // true when a reviewed plan is awaiting explicit user approval
	approvedPlanRun bool                // true during a turn where user approved pending plan

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
func New(cfg config.Config, database *db.DB, sessions *session.Service, registry *tools.Registry, mainProv provider.Provider, progProv provider.Provider, permSvc *permission.Service, reviewerSpec *planner.ReviewerSpec) Model {
	return Model{
		keys:         DefaultKeyMap(),
		input:        NewInput(),
		msgs:         NewMessageList(),
		settings:     NewSettings(provider.Supported(), cfg.MainAgentProvider()),
		cfg:          cfg,
		database:     database,
		sessions:     sessions,
		registry:     registry,
		mainProv:     mainProv,
		progProv:     progProv,
		permSvc:      permSvc,
		reviewerSpec: reviewerSpec,
		progRef:      &programRef{}, // shared across Bubble Tea value copies
	}
}

// SetProgram stores a reference to the tea.Program for async command sending.
// Safe to call after tea.NewProgram because progRef is shared across copies.
func (m *Model) SetProgram(p *tea.Program) {
	m.progRef.Store(p)
}

// rebuildReviewerSpec recreates the reviewer spec from current config.
func rebuildReviewerSpec(cfg config.Config) *planner.ReviewerSpec {
	if !cfg.ReviewEnabled() {
		return nil
	}

	reviewerProvider := cfg.ReviewerAgentProvider()
	reviewerModel := cfg.ReviewerAgentModel()
	reviewProv, err := provider.New(reviewerProvider, cfg.APIKeyFor(reviewerProvider), reviewerModel)
	if err != nil {
		return nil
	}
	configureOpenAIOAuthMode(reviewerProvider, reviewProv, cfg)

	return &planner.ReviewerSpec{Provider: reviewProv, Model: reviewerModel}
}

func rebuildProgrammerProvider(cfg config.Config) (provider.Provider, error) {
	progProvider := cfg.ProgrammerAgentProvider()
	progModel := cfg.ProgrammerAgentModel()
	prov, err := provider.New(progProvider, cfg.APIKeyFor(progProvider), progModel)
	if err != nil {
		return nil, err
	}
	configureOpenAIOAuthMode(progProvider, prov, cfg)
	return prov, nil
}

func configureOpenAIOAuthMode(providerName string, prov provider.Provider, cfg config.Config) {
	if providerName != "openai" {
		return
	}
	oai, ok := prov.(*provider.OpenAIProvider)
	if !ok {
		return
	}
	authCfg, ok := cfg.AuthFor("openai")
	if !ok || authCfg.Type != "oauth" {
		oai.SetOAuthCodexMode(false, "")
		return
	}
	oai.SetOAuthCodexMode(true, authCfg.AccountID)
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
		if m.mainProv != nil && m.mainProv.Name() == "copilot" {
			m.mainProv.SetAPIKey(msg.token)
		}
		if m.progProv != nil && m.progProv.Name() == "copilot" {
			m.progProv.SetAPIKey(msg.token)
		}
		if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil && m.reviewerSpec.Provider.Name() == "copilot" {
			m.reviewerSpec.Provider.SetAPIKey(msg.token)
		}

		// Persist to config file.
		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, nil
		}

		m.settings.SetFeedback("Copilot authenticated successfully", false)
		m.settings.SetView(settingsViewProviderMenu)
		return m, nil

	case openAIDeviceCodeMsg:
		m.settings.HandleOpenAIDeviceCode(msg.userCode, msg.url, msg.err)
		if msg.err != nil {
			return m, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.settings.SetOpenAICancel(cancel)
		deviceAuthID := msg.deviceAuthID
		userCode := msg.userCode
		interval := msg.interval
		return m, func() tea.Msg {
			token, err := auth.PollOpenAIForToken(ctx, deviceAuthID, userCode, interval)
			if err != nil {
				return openAIAuthMsg{err: err}
			}
			return openAIAuthMsg{
				accessToken:  token.AccessToken,
				refreshToken: token.RefreshToken,
				expiresIn:    token.ExpiresIn,
				accountID:    token.AccountID,
			}
		}

	case openAIBrowserStartMsg:
		m.settings.HandleOpenAIBrowserStart(msg.url, msg.err)
		if msg.err != nil {
			return m, nil
		}
		if msg.openErr != nil {
			m.settings.SetOpenAIWarning(fmt.Sprintf("Couldn't auto-open browser (%s). Open the URL below manually.", msg.openErr.Error()))
		}
		ctx, cancel := context.WithCancel(context.Background())
		m.settings.SetOpenAICancel(cancel)
		return m, func() tea.Msg {
			token, err := auth.WaitOpenAIBrowserAuth(ctx)
			if err != nil {
				return openAIAuthMsg{err: err}
			}
			return openAIAuthMsg{
				accessToken:  token.AccessToken,
				refreshToken: token.RefreshToken,
				expiresIn:    token.ExpiresIn,
				accountID:    token.AccountID,
			}
		}

	case openAIAuthMsg:
		m.settings.HandleOpenAIAuth(msg.err)
		if msg.err != nil {
			return m, nil
		}

		expiresAt := time.Now().Add(time.Duration(msg.expiresIn) * time.Second).Unix()
		m.cfg.SetAuthFor("openai", config.ProviderAuth{
			Type:         "oauth",
			AccessToken:  msg.accessToken,
			RefreshToken: msg.refreshToken,
			ExpiresAt:    expiresAt,
			AccountID:    msg.accountID,
		})
		m.cfg.SetAPIKeyFor("openai", msg.accessToken)
		if m.mainProv != nil && m.mainProv.Name() == "openai" {
			m.mainProv.SetAPIKey(msg.accessToken)
		}
		if m.progProv != nil && m.progProv.Name() == "openai" {
			m.progProv.SetAPIKey(msg.accessToken)
		}
		if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil && m.reviewerSpec.Provider.Name() == "openai" {
			m.reviewerSpec.Provider.SetAPIKey(msg.accessToken)
		}
		configureOpenAIOAuthMode("openai", m.mainProv, m.cfg)
		configureOpenAIOAuthMode("openai", m.progProv, m.cfg)
		if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil {
			configureOpenAIOAuthMode("openai", m.reviewerSpec.Provider, m.cfg)
		}

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, nil
		}

		m.settings.SetFeedback("OpenAI authenticated successfully", false)
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
	mainProvider := m.cfg.MainAgentProvider()
	if err := m.maybeRefreshProviderOAuth(mainProvider); err != nil {
		m.msgs.Add(message.System,
			fmt.Sprintf("OpenAI authentication refresh failed: %s. Press ctrl+k to re-authenticate.", err.Error()))
		return nil
	}

	// Check if API key is configured for the main agent provider.
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
	m.approvedPlanRun = m.planAwaitingAck && isPlanApprovalPrompt(prompt)

	eventCh := m.startMainOrchestrator(ctx, history, sessionID)
	return m.forwardEvents(eventCh)
}

func (m *Model) forwardEvents(eventCh <-chan agent.Event) tea.Cmd {
	program := m.progRef.Load()

	return func() tea.Msg {
		event, ok := <-eventCh
		if !ok {
			return agentEventMsg{event: agent.Event{Type: agent.EventAgentDone}}
		}

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

func (m *Model) startMainOrchestrator(ctx context.Context, history []message.Message, sessionID string) <-chan agent.Event {
	augmented := append([]message.Message{}, history...)
	approval := "false"
	if m.approvedPlanRun {
		approval = "true"
	}
	note := "ORCHESTRATOR_CONTEXT\n" +
		fmt.Sprintf("always_review_mode: %t\n", m.cfg.AlwaysReview) +
		fmt.Sprintf("pending_reviewed_plan: %t\n", m.planAwaitingAck) +
		"user_approved_latest_plan: " + approval
	if m.planAwaitingAck && m.pendingPlan != "" {
		note += "\n\nLatest reviewed plan:\n" + m.pendingPlan
	}
	augmented = append(augmented, message.NewUserMessage(sessionID, note))

	ag := agent.New(agent.Config{
		Provider:      m.mainProv,
		Registry:      m.registry.ReadOnly(),
		PermSvc:       nil,
		WorkDir:       m.cfg.WorkDir,
		Model:         m.cfg.MainAgentModel(),
		MaxTokens:     m.cfg.MaxTokens,
		MaxIterations: m.cfg.MaxIterations,
		SystemPrompt:  prompt.BuildOrchestratorPrompt(m.cfg.WorkDir, m.registry),
	})
	m.activeRunRole = runRoleMain
	return ag.Run(ctx, augmented, sessionID)
}

func (m *Model) startPlanner(ctx context.Context, history []message.Message, sessionID string) <-chan agent.Event {
	pl := planner.New(planner.Config{
		MainProvider:  m.mainProv,
		ReviewerSpec:  m.reviewerSpec,
		Registry:      m.registry,
		WorkDir:       m.cfg.WorkDir,
		MainModel:     m.cfg.MainAgentModel(),
		MaxTokens:     m.cfg.MaxTokens,
		MaxIterations: m.cfg.MaxIterations,
		ReviewRounds:  m.cfg.ReviewIterations,
	})
	m.activeRunRole = runRolePlanner
	return pl.Run(ctx, history, sessionID)
}

func (m *Model) startProgrammer(ctx context.Context, history []message.Message, sessionID, plan string) <-chan agent.Event {
	augmented := append([]message.Message{}, history...)
	instruction := "Implement the approved plan below using available tools.\n\nApproved plan:\n" + plan
	augmented = append(augmented, message.NewUserMessage(sessionID, instruction))

	ag := agent.New(agent.Config{
		Provider:      m.progProv,
		Registry:      m.registry,
		PermSvc:       m.permSvc,
		WorkDir:       m.cfg.WorkDir,
		Model:         m.cfg.ProgrammerAgentModel(),
		MaxTokens:     m.cfg.MaxTokens,
		MaxIterations: m.cfg.MaxIterations,
	})
	m.activeRunRole = runRoleProgrammer
	return ag.Run(ctx, augmented, sessionID)
}

type orchestratorDecision struct {
	Action  string
	Message string
	Plan    string
}

func parseOrchestratorDecision(content string) orchestratorDecision {
	decision := orchestratorDecision{Action: "RESPOND", Message: strings.TrimSpace(content)}
	lines := strings.Split(content, "\n")

	planStart := -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "ACTION:"):
			decision.Action = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "ACTION:")))
		case strings.HasPrefix(line, "MESSAGE:"):
			decision.Message = strings.TrimSpace(strings.TrimPrefix(line, "MESSAGE:"))
		case strings.HasPrefix(line, "PLAN:"):
			planStart = i
		}
	}

	if planStart >= 0 && planStart+1 < len(lines) {
		decision.Plan = strings.TrimSpace(strings.Join(lines[planStart+1:], "\n"))
	}
	if decision.Message == "" {
		decision.Message = strings.TrimSpace(content)
	}

	switch decision.Action {
	case "RESPOND", "RUN_REVIEW_LOOP", "CALL_PROGRAMMER":
	default:
		decision.Action = "RESPOND"
	}

	return decision
}

func isPlanApprovalPrompt(prompt string) bool {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	if normalized == "" {
		return false
	}

	approvals := []string{
		"yes",
		"y",
		"approve",
		"approved",
		"looks good",
		"go ahead",
		"do it",
		"proceed",
		"ship it",
	}
	for _, a := range approvals {
		if normalized == a || strings.Contains(normalized, a) {
			return true
		}
	}

	return false
}

// handleAgentEvent processes events from the agent loop.
func (m Model) handleAgentEvent(event agent.Event) (tea.Model, tea.Cmd) {
	switch event.Type {
	case agent.EventStreamText:
		if m.activeRunRole == runRoleMain {
			return m, nil
		}
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
		if m.activeRunRole == runRoleMain {
			return m, nil
		}
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
		switch m.activeRunRole {
		case runRoleMain:
			if event.FinalMessage == nil {
				m.thinking = false
				m.streamBuf = ""
				m.activeRunRole = runRoleNone
				m.approvedPlanRun = false
				return m, m.listenForPermissions()
			}

			decision := parseOrchestratorDecision(event.FinalMessage.Content)
			if decision.Message == "" {
				decision.Message = "Done."
			}

			switch decision.Action {
			case "RUN_REVIEW_LOOP":
				if m.reviewerSpec == nil {
					msg := message.NewAssistantMessage(event.FinalMessage.SessionID, "Reviewer is not configured; continuing without review loop.", nil)
					msg.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(msg); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(msg)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}

				reviewerProvider := m.cfg.ReviewerAgentProvider()
				if err := m.maybeRefreshProviderOAuth(reviewerProvider); err != nil {
					failed := message.NewAssistantMessage(event.FinalMessage.SessionID, fmt.Sprintf("OpenAI authentication refresh failed for reviewer provider %q: %s", reviewerProvider, err.Error()), nil)
					failed.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(failed); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(failed)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}

				history, err := m.sessions.GetMessages()
				if err != nil {
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, func() tea.Msg { return errMsg(fmt.Errorf("loading history: %w", err)) }
				}

				ctx, cancel := context.WithCancel(context.Background())
				m.agentCancel = cancel
				m.streamBuf = ""
				eventCh := m.startPlanner(ctx, history, m.sessions.CurrentID())
				return m, m.forwardEvents(eventCh)

			case "CALL_PROGRAMMER":
				if !m.approvedPlanRun {
					denied := message.NewAssistantMessage(event.FinalMessage.SessionID, "I can only call the programmer after you explicitly approve the reviewed plan.", nil)
					denied.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(denied); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(denied)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}

				plan := strings.TrimSpace(decision.Plan)
				if plan == "" {
					plan = strings.TrimSpace(m.pendingPlan)
				}
				if plan == "" {
					noPlan := message.NewAssistantMessage(event.FinalMessage.SessionID, "No approved plan is available to implement.", nil)
					noPlan.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(noPlan); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(noPlan)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}

				progProvider := m.cfg.ProgrammerAgentProvider()
				if err := m.maybeRefreshProviderOAuth(progProvider); err != nil {
					missing := message.NewAssistantMessage(event.FinalMessage.SessionID, fmt.Sprintf("OpenAI authentication refresh failed for programmer provider %q: %s", progProvider, err.Error()), nil)
					missing.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(missing); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(missing)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}
				if m.cfg.APIKeyFor(progProvider) == "" {
					missing := message.NewAssistantMessage(event.FinalMessage.SessionID, fmt.Sprintf("No API key configured for programmer provider %q.", progProvider), nil)
					missing.Model = m.cfg.MainAgentModel()
					if err := m.sessions.AddMessage(missing); err != nil {
						m.err = err
					}
					m.msgs.AddMessage(missing)
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, m.listenForPermissions()
				}

				history, err := m.sessions.GetMessages()
				if err != nil {
					m.thinking = false
					m.streamBuf = ""
					m.activeRunRole = runRoleNone
					m.approvedPlanRun = false
					return m, func() tea.Msg { return errMsg(fmt.Errorf("loading history: %w", err)) }
				}

				ctx, cancel := context.WithCancel(context.Background())
				m.agentCancel = cancel
				m.planAwaitingAck = false
				m.pendingPlan = ""
				m.streamBuf = ""
				eventCh := m.startProgrammer(ctx, history, m.sessions.CurrentID(), plan)
				return m, m.forwardEvents(eventCh)

			default:
				assistantMsg := message.NewAssistantMessage(event.FinalMessage.SessionID, decision.Message, nil)
				assistantMsg.Model = m.cfg.MainAgentModel()
				assistantMsg.InputTokens = event.FinalMessage.InputTokens
				assistantMsg.OutputTokens = event.FinalMessage.OutputTokens
				assistantMsg.TotalTokens = event.FinalMessage.TotalTokens
				if err := m.sessions.AddMessage(assistantMsg); err != nil {
					m.err = err
				}
				m.tokenTotal += assistantMsg.TotalTokens
				if assistantMsg.Model != "" {
					if m.tokenTotalByModel == nil {
						m.tokenTotalByModel = make(map[string]int)
					}
					m.tokenTotalByModel[assistantMsg.Model] += assistantMsg.TotalTokens
				}
				m.msgs.AddMessage(assistantMsg)
				m.thinking = false
				m.streamBuf = ""
				m.activeRunRole = runRoleNone
				m.approvedPlanRun = false
				return m, m.listenForPermissions()
			}

		case runRolePlanner:
			m.thinking = false
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
				m.msgs.FinalizeStreaming(event.FinalMessage.Content)
				m.pendingPlan = event.FinalMessage.Content
				m.planAwaitingAck = true
			}
			m.streamBuf = ""
			m.activeRunRole = runRoleNone
			m.approvedPlanRun = false
			return m, m.listenForPermissions()

		case runRoleProgrammer, runRoleNone:
			m.thinking = false
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
				m.msgs.FinalizeStreaming(event.FinalMessage.Content)
			}
			m.streamBuf = ""
			m.activeRunRole = runRoleNone
			m.approvedPlanRun = false
			return m, m.listenForPermissions()
		}

	case agent.EventAgentError:
		m.thinking = false
		m.streamBuf = ""
		m.activeRunRole = runRoleNone
		m.approvedPlanRun = false
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

	// Handle transition to OpenAI auth (trigger browser OAuth flow)
	if prevView != settingsViewOpenAIAuth && m.settings.view == settingsViewOpenAIAuth {
		return m, func() tea.Msg {
			authURL, err := auth.StartOpenAIBrowserAuth()
			if err != nil {
				return openAIBrowserStartMsg{err: err}
			}
			openErr := openExternalURL(authURL)
			return openAIBrowserStartMsg{url: authURL, openErr: openErr}
		}
	}

	// Handle transition to main agent model selection (trigger fetch)
	if prevView != settingsViewAgentMainModels && m.settings.view == settingsViewAgentMainModels {
		provName := m.settings.AgentProviderPick()
		if err := m.maybeRefreshProviderOAuth(provName); err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider auth error: %s", err.Error()))
			return m, nil
		}
		apiKey := m.cfg.APIKeyFor(provName)
		tmpProv, err := provider.New(provName, apiKey, "")
		if err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
			return m, nil
		}
		configureOpenAIOAuthMode(provName, tmpProv, m.cfg)
		return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
	}

	// Handle transition to reviewer model selection (trigger fetch)
	if prevView != settingsViewAgentReviewerModels && m.settings.view == settingsViewAgentReviewerModels {
		provName := m.settings.AgentProviderPick()
		if err := m.maybeRefreshProviderOAuth(provName); err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider auth error: %s", err.Error()))
			return m, nil
		}
		apiKey := m.cfg.APIKeyFor(provName)
		tmpProv, err := provider.New(provName, apiKey, "")
		if err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
			return m, nil
		}
		configureOpenAIOAuthMode(provName, tmpProv, m.cfg)
		return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
	}

	// Handle transition to programmer model selection (trigger fetch)
	if prevView != settingsViewAgentProgrammerModels && m.settings.view == settingsViewAgentProgrammerModels {
		provName := m.settings.AgentProviderPick()
		if err := m.maybeRefreshProviderOAuth(provName); err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider auth error: %s", err.Error()))
			return m, nil
		}
		apiKey := m.cfg.APIKeyFor(provName)
		tmpProv, err := provider.New(provName, apiKey, "")
		if err != nil {
			m.settings.HandleModelsLoaded(nil, fmt.Errorf("provider error: %s", err.Error()))
			return m, nil
		}
		configureOpenAIOAuthMode(provName, tmpProv, m.cfg)
		return m, fetchModelsCmd(context.Background(), tmpProv.ListModels)
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
		if selectedProvider == "openai" {
			m.cfg.ClearAuthFor("openai")
			configureOpenAIOAuthMode("openai", m.mainProv, m.cfg)
			configureOpenAIOAuthMode("openai", m.progProv, m.cfg)
			if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil {
				configureOpenAIOAuthMode("openai", m.reviewerSpec.Provider, m.cfg)
			}
		}
		if m.mainProv != nil && selectedProvider == m.mainProv.Name() {
			m.mainProv.SetAPIKey(apiKey)
		}
		if m.progProv != nil && selectedProvider == m.progProv.Name() {
			m.progProv.SetAPIKey(apiKey)
		}
		if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil && selectedProvider == m.reviewerSpec.Provider.Name() {
			m.reviewerSpec.Provider.SetAPIKey(apiKey)
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

	// Handle review rounds save on enter in review rounds view
	if m.settings.view == settingsViewReviewRounds && msg.String() == "enter" {
		val := m.settings.ReviewRoundValue()
		if val == 0 {
			return m, cmd
		}

		m.cfg.ReviewIterations = val

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Review rounds set to %d", val), false)
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
		m.mainProv = newProv
		configureOpenAIOAuthMode(provName, m.mainProv, m.cfg)
		m.reviewerSpec = rebuildReviewerSpec(m.cfg)
		if rebuiltProg, progErr := rebuildProgrammerProvider(m.cfg); progErr == nil {
			m.progProv = rebuiltProg
		}

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Main agent set to %s:%s", provName, selected), false)
		m.settings.view = settingsViewAgents
		return m, cmd
	}

	// Handle reviewer model selection on enter
	if m.settings.view == settingsViewAgentReviewerModels && msg.String() == "enter" {
		selected := m.settings.SelectedModel()
		if selected == "" {
			return m, cmd
		}

		provName := m.settings.AgentProviderPick()
		m.cfg.Agents.Reviewer = &config.AgentSpec{Provider: provName, Model: selected}
		m.reviewerSpec = rebuildReviewerSpec(m.cfg)

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Review agent set to %s:%s", provName, selected), false)
		m.settings.view = settingsViewAgents
		return m, cmd
	}

	// Handle programmer model selection on enter
	if m.settings.view == settingsViewAgentProgrammerModels && msg.String() == "enter" {
		selected := m.settings.SelectedModel()
		if selected == "" {
			return m, cmd
		}

		provName := m.settings.AgentProviderPick()
		m.cfg.Agents.Programmer = &config.AgentSpec{Provider: provName, Model: selected}

		newProg, err := provider.New(provName, m.cfg.APIKeyFor(provName), selected)
		if err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Provider error: %s", err.Error()), true)
			return m, cmd
		}
		m.progProv = newProg
		configureOpenAIOAuthMode(provName, m.progProv, m.cfg)

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback(fmt.Sprintf("Programmer agent set to %s:%s", provName, selected), false)
		m.settings.view = settingsViewAgents
		return m, cmd
	}

	// Disable reviewer from reviewer model selection.
	if m.settings.view == settingsViewAgentReviewerModels && (msg.String() == "d" || msg.String() == "D") {
		m.cfg.Agents.Reviewer = nil
		m.reviewerSpec = nil

		if err := config.Save(m.cfg); err != nil {
			m.settings.SetFeedback(fmt.Sprintf("Save failed: %s", err.Error()), true)
			return m, cmd
		}

		m.settings.SetFeedback("Review agent disabled", false)
		m.settings.view = settingsViewAgents
		return m, cmd
	}

	return m, cmd
}

func (m *Model) maybeRefreshProviderOAuth(providerName string) error {
	if providerName != "openai" {
		return nil
	}

	authCfg, ok := m.cfg.AuthFor(providerName)
	if !ok || authCfg.Type != "oauth" || authCfg.RefreshToken == "" {
		return nil
	}

	if authCfg.AccessToken != "" && authCfg.ExpiresAt > time.Now().Unix()+60 {
		return nil
	}

	tok, err := auth.RefreshOpenAIToken(context.Background(), authCfg.RefreshToken)
	if err != nil {
		return err
	}

	expiresAt := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	m.cfg.SetAuthFor(providerName, config.ProviderAuth{
		Type:         "oauth",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    tok.AccountID,
	})
	m.cfg.SetAPIKeyFor(providerName, tok.AccessToken)

	if m.mainProv != nil && m.mainProv.Name() == providerName {
		m.mainProv.SetAPIKey(tok.AccessToken)
		configureOpenAIOAuthMode(providerName, m.mainProv, m.cfg)
	}
	if m.progProv != nil && m.progProv.Name() == providerName {
		m.progProv.SetAPIKey(tok.AccessToken)
		configureOpenAIOAuthMode(providerName, m.progProv, m.cfg)
	}
	if m.reviewerSpec != nil && m.reviewerSpec.Provider != nil && m.reviewerSpec.Provider.Name() == providerName {
		m.reviewerSpec.Provider.SetAPIKey(tok.AccessToken)
		configureOpenAIOAuthMode(providerName, m.reviewerSpec.Provider, m.cfg)
	}

	if err := config.Save(m.cfg); err != nil {
		return fmt.Errorf("saving refreshed auth: %w", err)
	}

	return nil
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
		var reviewer *agentEntry
		if m.cfg.Agents.Reviewer != nil {
			reviewer = &agentEntry{Provider: m.cfg.ReviewerAgentProvider(), Model: m.cfg.ReviewerAgentModel()}
		}
		programmer := &agentEntry{Provider: m.cfg.ProgrammerAgentProvider(), Model: m.cfg.ProgrammerAgentModel()}
		if programmer.Provider == "" || programmer.Model == "" {
			programmer = nil
		}
		selectedProvider := m.settings.SelectedProvider()
		if selectedProvider == "" {
			selectedProvider = m.cfg.MainAgentProvider()
		}
		inputView = m.settings.View(m.width, m.cfg.APIKeyFor(selectedProvider), m.cfg.MaxIterations, m.cfg.ReviewIterations, mainAgent, reviewer, programmer)
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
