package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// settingsView represents which sub-view of the settings overlay is active.
type settingsView int

const (
	settingsViewMenu settingsView = iota
	settingsViewProviders
	settingsViewProviderMenu
	settingsViewAPIKey
	settingsViewMaxIter
	settingsViewReviewRounds
	settingsViewCopilotAuth
	settingsViewAgents
	settingsViewAgentMain
	settingsViewAgentMainModels
	settingsViewAgentReviewer
	settingsViewAgentReviewerModels
)

// Settings holds the state for the settings overlay.
type Settings struct {
	view     settingsView
	apiInput textinput.Model

	// Provider selection state
	providers         []string
	providerCursor    int
	selectedProvider  string
	providerForModels string

	// Iteration inputs
	maxIterInput     textinput.Model
	reviewRoundInput textinput.Model

	// Model selection state
	models       []string
	modelCursor  int
	modelsErr    error
	loadingModel bool

	// Feedback messages
	feedback    string
	feedbackErr bool

	// Copilot device flow state
	copilotUserCode string
	copilotURL      string
	copilotPolling  bool
	copilotCancel   context.CancelFunc
	copilotErr      error

	// Agent configuration state
	agentProviderCursor int
	agentProviderPick   string
	modelSelectTarget   modelSelectFor
}

// agentEntry is a provider+model pair displayed in settings.
type agentEntry struct {
	Provider string
	Model    string
}

type modelSelectFor int

const (
	modelSelectAgentMain modelSelectFor = iota
	modelSelectAgentReviewer
)

// NewSettings creates a new settings component.
func NewSettings(providers []string, currentProvider string) Settings {
	ti := textinput.New()
	ti.Placeholder = "sk-..."
	ti.CharLimit = 256
	ti.Width = 60
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '*'

	mi := textinput.New()
	mi.Placeholder = "25"
	mi.CharLimit = 5
	mi.Width = 10

	ri := textinput.New()
	ri.Placeholder = "3"
	ri.CharLimit = 3
	ri.Width = 10

	return Settings{
		view:             settingsViewMenu,
		apiInput:         ti,
		maxIterInput:     mi,
		reviewRoundInput: ri,
		providers:        providers,
		selectedProvider: currentProvider,
	}
}

// --- Async message types for settings ---

type modelsLoadedMsg struct {
	models []string
	err    error
}

type copilotAuthMsg struct {
	token string
	err   error
}

type copilotDeviceCodeMsg struct {
	userCode   string
	url        string
	deviceCode string
	interval   int
	err        error
}

// Update handles key events in the settings overlay.
func (s Settings) Update(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch s.view {
	case settingsViewMenu:
		return s.updateMenu(msg)
	case settingsViewProviders:
		return s.updateProviders(msg)
	case settingsViewProviderMenu:
		return s.updateProviderMenu(msg)
	case settingsViewAPIKey:
		return s.updateAPIKey(msg)
	case settingsViewMaxIter:
		return s.updateMaxIter(msg)
	case settingsViewReviewRounds:
		return s.updateReviewRounds(msg)
	case settingsViewCopilotAuth:
		return s.updateCopilotAuth(msg)
	case settingsViewAgents:
		return s.updateAgents(msg)
	case settingsViewAgentMain:
		return s.updateAgentMain(msg)
	case settingsViewAgentMainModels:
		return s.updateAgentMainModels(msg)
	case settingsViewAgentReviewer:
		return s.updateAgentReviewer(msg)
	case settingsViewAgentReviewerModels:
		return s.updateAgentReviewerModels(msg)
	}
	return s, false, nil
}

func (s Settings) updateMenu(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+k":
		return s, true, nil
	case "1", "p", "P":
		s.view = settingsViewProviders
		s.feedback = ""
		s.providerCursor = 0
		return s, false, nil
	case "2", "m", "M":
		s.view = settingsViewMaxIter
		s.feedback = ""
		s.maxIterInput.SetValue("")
		s.maxIterInput.Focus()
		return s, false, s.maxIterInput.Cursor.BlinkCmd()
	case "3", "r", "R":
		s.view = settingsViewReviewRounds
		s.feedback = ""
		s.reviewRoundInput.SetValue("")
		s.reviewRoundInput.Focus()
		return s, false, s.reviewRoundInput.Cursor.BlinkCmd()
	case "4", "a", "A":
		s.view = settingsViewAgents
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateProviders(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
		return s, false, nil
	case "up", "k":
		if s.providerCursor > 0 {
			s.providerCursor--
		}
		return s, false, nil
	case "down", "j":
		if s.providerCursor < len(s.providers)-1 {
			s.providerCursor++
		}
		return s, false, nil
	case "enter":
		if len(s.providers) == 0 {
			return s, false, nil
		}
		s.selectedProvider = s.providers[s.providerCursor]
		s.view = settingsViewProviderMenu
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateProviderMenu(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewProviders
		return s, false, nil
	case "1", "a", "A":
		if s.selectedProvider == "copilot" {
			s.view = settingsViewCopilotAuth
			s.feedback = ""
			s.copilotUserCode = ""
			s.copilotURL = ""
			s.copilotPolling = false
			s.copilotErr = nil
			return s, false, nil
		}
		s.view = settingsViewAPIKey
		s.feedback = ""
		s.apiInput.SetValue("")
		s.apiInput.Focus()
		return s, false, s.apiInput.Cursor.BlinkCmd()
	}
	return s, false, nil
}

func (s Settings) updateAPIKey(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
		s.apiInput.Blur()
		return s, false, nil
	case "enter":
		val := strings.TrimSpace(s.apiInput.Value())
		if val == "" {
			s.feedback = "API key cannot be empty"
			s.feedbackErr = true
			return s, false, nil
		}
		s.apiInput.Blur()
		return s, false, nil
	}

	var cmd tea.Cmd
	s.apiInput, cmd = s.apiInput.Update(msg)
	return s, false, cmd
}

func (s Settings) updateMaxIter(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
		s.maxIterInput.Blur()
		return s, false, nil
	case "enter":
		val := strings.TrimSpace(s.maxIterInput.Value())
		if val == "" {
			s.feedback = "Value cannot be empty"
			s.feedbackErr = true
			return s, false, nil
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			s.feedback = "Enter a positive integer"
			s.feedbackErr = true
			return s, false, nil
		}
		s.maxIterInput.Blur()
		return s, false, nil
	}

	return s.updateNumericInput(msg, s.maxIterInput, func(ti textinput.Model) Settings {
		s.maxIterInput = ti
		return s
	})
}

func (s Settings) updateReviewRounds(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
		s.reviewRoundInput.Blur()
		return s, false, nil
	case "enter":
		val := strings.TrimSpace(s.reviewRoundInput.Value())
		if val == "" {
			s.feedback = "Value cannot be empty"
			s.feedbackErr = true
			return s, false, nil
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			s.feedback = "Enter a positive integer"
			s.feedbackErr = true
			return s, false, nil
		}
		s.reviewRoundInput.Blur()
		return s, false, nil
	}

	return s.updateNumericInput(msg, s.reviewRoundInput, func(ti textinput.Model) Settings {
		s.reviewRoundInput = ti
		return s
	})
}

func (s Settings) updateNumericInput(msg tea.KeyMsg, ti textinput.Model, assign func(textinput.Model) Settings) (Settings, bool, tea.Cmd) {
	if len(msg.String()) == 1 && msg.String()[0] >= '0' && msg.String()[0] <= '9' {
		var cmd tea.Cmd
		ti, cmd = ti.Update(msg)
		s = assign(ti)
		return s, false, cmd
	}

	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		var cmd tea.Cmd
		ti, cmd = ti.Update(msg)
		s = assign(ti)
		return s, false, cmd
	}

	return s, false, nil
}

func (s Settings) updateCopilotAuth(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if s.copilotCancel != nil {
			s.copilotCancel()
			s.copilotCancel = nil
		}
		s.copilotPolling = false
		s.copilotErr = nil
		s.view = settingsViewProviderMenu
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateAgents(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
		return s, false, nil
	case "1", "m", "M":
		s.view = settingsViewAgentMain
		s.agentProviderCursor = 0
		s.feedback = ""
		return s, false, nil
	case "2", "r", "R":
		s.view = settingsViewAgentReviewer
		s.agentProviderCursor = 0
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateAgentMain(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewAgents
		return s, false, nil
	case "up", "k":
		if s.agentProviderCursor > 0 {
			s.agentProviderCursor--
		}
		return s, false, nil
	case "down", "j":
		if s.agentProviderCursor < len(s.providers)-1 {
			s.agentProviderCursor++
		}
		return s, false, nil
	case "enter":
		if len(s.providers) == 0 {
			return s, false, nil
		}
		s.agentProviderPick = s.providers[s.agentProviderCursor]
		s.providerForModels = s.agentProviderPick
		s.modelSelectTarget = modelSelectAgentMain
		s.view = settingsViewAgentMainModels
		s.modelCursor = 0
		s.models = nil
		s.modelsErr = nil
		s.loadingModel = true
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateAgentMainModels(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	return s.updateModelSelection(msg, settingsViewAgentMain)
}

func (s Settings) updateAgentReviewer(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewAgents
		return s, false, nil
	case "up", "k":
		if s.agentProviderCursor > 0 {
			s.agentProviderCursor--
		}
		return s, false, nil
	case "down", "j":
		if s.agentProviderCursor < len(s.providers)-1 {
			s.agentProviderCursor++
		}
		return s, false, nil
	case "enter":
		if len(s.providers) == 0 {
			return s, false, nil
		}
		s.agentProviderPick = s.providers[s.agentProviderCursor]
		s.providerForModels = s.agentProviderPick
		s.modelSelectTarget = modelSelectAgentReviewer
		s.view = settingsViewAgentReviewerModels
		s.modelCursor = 0
		s.models = nil
		s.modelsErr = nil
		s.loadingModel = true
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

func (s Settings) updateAgentReviewerModels(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	return s.updateModelSelection(msg, settingsViewAgentReviewer)
}

func (s Settings) updateModelSelection(msg tea.KeyMsg, backView settingsView) (Settings, bool, tea.Cmd) {
	if s.loadingModel {
		if msg.String() == "esc" {
			s.view = backView
			s.loadingModel = false
			return s, false, nil
		}
		return s, false, nil
	}
	if s.modelsErr != nil {
		if msg.String() == "esc" {
			s.view = backView
			s.modelsErr = nil
			return s, false, nil
		}
		return s, false, nil
	}

	switch msg.String() {
	case "esc":
		s.view = backView
		return s, false, nil
	case "up", "k":
		if s.modelCursor > 0 {
			s.modelCursor--
		}
		return s, false, nil
	case "down", "j":
		if s.modelCursor < len(s.models)-1 {
			s.modelCursor++
		}
		return s, false, nil
	case "enter", "d", "D":
		// model.go handles persistence actions for these keys.
		return s, false, nil
	}

	return s, false, nil
}

// MaxIterValue returns the max iteration input as int, or 0 if invalid.
func (s Settings) MaxIterValue() int {
	val := strings.TrimSpace(s.maxIterInput.Value())
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// ReviewRoundValue returns the review round input as int, or 0 if invalid.
func (s Settings) ReviewRoundValue() int {
	val := strings.TrimSpace(s.reviewRoundInput.Value())
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func (s *Settings) HandleModelsLoaded(models []string, err error) {
	s.loadingModel = false
	if err != nil {
		s.modelsErr = err
		return
	}
	s.models = models
	s.modelCursor = 0
}

func (s *Settings) SetFeedback(msg string, isErr bool) {
	s.feedback = msg
	s.feedbackErr = isErr
}

func (s *Settings) SetView(view settingsView) {
	s.view = view
}

func (s Settings) SelectedProvider() string {
	return s.selectedProvider
}

func (s Settings) SelectedModel() string {
	if len(s.models) > 0 && s.modelCursor < len(s.models) {
		return s.models[s.modelCursor]
	}
	return ""
}

func (s Settings) APIKeyValue() string {
	return strings.TrimSpace(s.apiInput.Value())
}

func (s Settings) AgentProviderPick() string {
	return s.agentProviderPick
}

func (s Settings) ModelSelectTarget() modelSelectFor {
	return s.modelSelectTarget
}

func (s *Settings) HandleCopilotDeviceCode(userCode, url string, err error) {
	if err != nil {
		s.copilotErr = err
		s.copilotPolling = false
		return
	}
	s.copilotUserCode = userCode
	s.copilotURL = url
	s.copilotPolling = true
}

func (s *Settings) HandleCopilotAuth(_ string, err error) {
	s.copilotPolling = false
	if s.copilotCancel != nil {
		s.copilotCancel()
		s.copilotCancel = nil
	}
	if err != nil {
		s.copilotErr = err
	}
}

func (s *Settings) SetCopilotCancel(cancel context.CancelFunc) {
	s.copilotCancel = cancel
}

// View renders the settings overlay.
func (s Settings) View(width int, currentKey string, currentMaxIter int, currentReviewRounds int, mainAgent agentEntry, reviewer *agentEntry) string {
	innerWidth := width - 6

	var content string
	switch s.view {
	case settingsViewMenu:
		content = s.viewMenu(currentMaxIter, currentReviewRounds, mainAgent, reviewer)
	case settingsViewProviders:
		content = s.viewProviders()
	case settingsViewProviderMenu:
		content = s.viewProviderMenu(currentKey)
	case settingsViewAPIKey:
		content = s.viewAPIKey(innerWidth)
	case settingsViewMaxIter:
		content = s.viewMaxIter(innerWidth, currentMaxIter)
	case settingsViewReviewRounds:
		content = s.viewReviewRounds(innerWidth, currentReviewRounds)
	case settingsViewCopilotAuth:
		content = s.viewCopilotAuth()
	case settingsViewAgents:
		content = s.viewAgents(mainAgent, reviewer)
	case settingsViewAgentMain:
		content = s.viewAgentMain(mainAgent)
	case settingsViewAgentMainModels:
		content = s.viewAgentMainModels(mainAgent)
	case settingsViewAgentReviewer:
		content = s.viewAgentReviewer(reviewer)
	case settingsViewAgentReviewerModels:
		content = s.viewAgentReviewerModels(reviewer)
	}

	return settingsStyle.Width(innerWidth).Render(content)
}

func (s Settings) viewMenu(currentMaxIter int, currentReviewRounds int, mainAgent agentEntry, reviewer *agentEntry) string {
	title := settingsTitleStyle.Render("Settings")

	reviewerSummary := "disabled"
	if reviewer != nil {
		reviewerSummary = fmt.Sprintf("%s:%s", reviewer.Provider, reviewer.Model)
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  [1] Providers      %s\n", dimStyle.Render(s.selectedProvider))
	fmt.Fprintf(&b, "  [2] Max Iters      %s\n", dimStyle.Render(strconv.Itoa(currentMaxIter)))
	fmt.Fprintf(&b, "  [3] Review Rounds  %s\n", dimStyle.Render(strconv.Itoa(currentReviewRounds)))
	fmt.Fprintf(&b, "  [4] Agents         %s\n", dimStyle.Render(fmt.Sprintf("main=%s:%s, reviewer=%s", mainAgent.Provider, mainAgent.Model, reviewerSummary)))

	if s.feedback != "" {
		b.WriteString("\n")
		if s.feedbackErr {
			b.WriteString("  " + settingsErrorStyle.Render(s.feedback))
		} else {
			b.WriteString("  " + settingsSuccessStyle.Render(s.feedback))
		}
	}

	b.WriteString("\n\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("esc: close"))
	return b.String()
}

func (s Settings) viewProviders() string {
	title := settingsTitleStyle.Render("Providers")

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	if len(s.providers) == 0 {
		b.WriteString("  No providers available\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	for i, p := range s.providers {
		cursor := "  "
		style := settingsItemStyle
		if i == s.providerCursor {
			cursor = settingsCursorStyle.Render("> ")
			style = settingsSelectedStyle
		}
		b.WriteString("  " + cursor + style.Render(titleCase(p)) + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("up/down: navigate  enter: select  esc: back"))
	return b.String()
}

func (s Settings) viewProviderMenu(currentKey string) string {
	title := settingsTitleStyle.Render(fmt.Sprintf("Provider: %s", titleCase(s.selectedProvider)))

	masked := "(not set)"
	if currentKey != "" {
		if len(currentKey) > 8 {
			masked = currentKey[:3] + "..." + currentKey[len(currentKey)-4:]
		} else {
			masked = "****"
		}
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	if s.selectedProvider == "copilot" {
		authLabel := "Authenticate"
		if currentKey != "" {
			authLabel = "Re-authenticate"
		}
		fmt.Fprintf(&b, "  [1] %s  %s\n", authLabel, dimStyle.Render(masked))
	} else {
		fmt.Fprintf(&b, "  [1] API Key     %s\n", dimStyle.Render(masked))
	}
	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
	return b.String()
}

func (s Settings) viewAPIKey(width int) string {
	title := settingsTitleStyle.Render(fmt.Sprintf("Enter %s API Key", titleCase(s.selectedProvider)))
	s.apiInput.Width = width - 4
	if s.apiInput.Width < 20 {
		s.apiInput.Width = 20
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	b.WriteString("  " + s.apiInput.View() + "\n")

	if s.feedback != "" {
		b.WriteString("\n")
		if s.feedbackErr {
			b.WriteString("  " + settingsErrorStyle.Render(s.feedback))
		} else {
			b.WriteString("  " + settingsSuccessStyle.Render(s.feedback))
		}
	}

	b.WriteString("\n\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("enter: save  esc: back"))

	return b.String()
}

func (s Settings) viewMaxIter(width int, currentMaxIter int) string {
	title := settingsTitleStyle.Render("Max Agent Iterations")
	s.maxIterInput.Width = 10

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  Current: %s\n\n", dimStyle.Render(strconv.Itoa(currentMaxIter)))
	b.WriteString("  " + s.maxIterInput.View() + "\n")
	b.WriteString("\n\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("enter: save  esc: back"))
	return b.String()
}

func (s Settings) viewReviewRounds(width int, currentRounds int) string {
	title := settingsTitleStyle.Render("Plan Review Rounds")
	s.reviewRoundInput.Width = 10

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  Current: %s\n\n", dimStyle.Render(strconv.Itoa(currentRounds)))
	b.WriteString("  " + s.reviewRoundInput.View() + "\n")
	b.WriteString("\n\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("enter: save  esc: back"))
	return b.String()
}

func (s Settings) viewCopilotAuth() string {
	title := settingsTitleStyle.Render("GitHub Copilot Authentication")

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	if s.copilotErr != nil {
		b.WriteString("  " + settingsErrorStyle.Render(fmt.Sprintf("Error: %s", s.copilotErr.Error())))
		b.WriteString("\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	if s.copilotUserCode == "" {
		b.WriteString("  Requesting device code...\n")
		b.WriteString("\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: cancel"))
		return b.String()
	}

	b.WriteString("  1. Open this URL in your browser:\n\n")
	b.WriteString("     " + settingsSelectedStyle.Render(s.copilotURL) + "\n\n")
	b.WriteString("  2. Enter this code:\n\n")
	b.WriteString("     " + settingsTitleStyle.Render(s.copilotUserCode) + "\n\n")

	if s.copilotPolling {
		b.WriteString("  " + dimStyle.Render("Waiting for authorization...") + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("esc: cancel"))

	return b.String()
}

func (s Settings) viewAgents(mainAgent agentEntry, reviewer *agentEntry) string {
	title := settingsTitleStyle.Render("Agents")

	reviewerSummary := "disabled"
	if reviewer != nil {
		reviewerSummary = fmt.Sprintf("%s:%s", reviewer.Provider, reviewer.Model)
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  [1] Main Agent    %s\n", dimStyle.Render(fmt.Sprintf("%s:%s", mainAgent.Provider, mainAgent.Model)))
	fmt.Fprintf(&b, "  [2] Review Agent  %s\n", dimStyle.Render(reviewerSummary))
	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
	return b.String()
}

func (s Settings) viewAgentMain(mainAgent agentEntry) string {
	title := settingsTitleStyle.Render("Main Agent Provider")

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  Current: %s\n\n", dimStyle.Render(fmt.Sprintf("%s:%s", mainAgent.Provider, mainAgent.Model)))

	for i, p := range s.providers {
		cursor := "  "
		style := settingsItemStyle
		if i == s.agentProviderCursor {
			cursor = settingsCursorStyle.Render("> ")
			style = settingsSelectedStyle
		}
		suffix := ""
		if p == mainAgent.Provider {
			suffix = dimStyle.Render(" (current)")
		}
		b.WriteString("  " + cursor + style.Render(titleCase(p)) + suffix + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("up/down: navigate  enter: select  esc: back"))
	return b.String()
}

func (s Settings) viewAgentMainModels(mainAgent agentEntry) string {
	title := settingsTitleStyle.Render("Main Agent Model")
	return s.viewModelList(title, mainAgent.Model, false)
}

func (s Settings) viewAgentReviewer(reviewer *agentEntry) string {
	title := settingsTitleStyle.Render("Review Agent Provider")

	current := "disabled"
	if reviewer != nil {
		current = fmt.Sprintf("%s:%s", reviewer.Provider, reviewer.Model)
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  Current: %s\n\n", dimStyle.Render(current))

	for i, p := range s.providers {
		cursor := "  "
		style := settingsItemStyle
		if i == s.agentProviderCursor {
			cursor = settingsCursorStyle.Render("> ")
			style = settingsSelectedStyle
		}
		suffix := ""
		if reviewer != nil && p == reviewer.Provider {
			suffix = dimStyle.Render(" (current)")
		}
		b.WriteString("  " + cursor + style.Render(titleCase(p)) + suffix + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("up/down: navigate  enter: select  esc: back"))
	return b.String()
}

func (s Settings) viewAgentReviewerModels(reviewer *agentEntry) string {
	title := settingsTitleStyle.Render("Review Agent Model")
	current := ""
	if reviewer != nil {
		current = reviewer.Model
	}
	return s.viewModelList(title, current, true)
}

func (s Settings) viewModelList(title, currentModel string, allowDisable bool) string {
	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	if s.loadingModel {
		b.WriteString("  Loading models...\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	if s.modelsErr != nil {
		b.WriteString("  " + settingsErrorStyle.Render(fmt.Sprintf("Error: %s", s.modelsErr.Error())))
		b.WriteString("\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	if len(s.models) == 0 {
		fmt.Fprintf(&b, "  No models found for %s\n", titleCase(s.providerForModels))
		b.WriteString("\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	fmt.Fprintf(&b, "  %s\n\n", dimStyle.Render(titleCase(s.providerForModels)))

	maxVisible := 10
	if maxVisible > len(s.models) {
		maxVisible = len(s.models)
	}

	start := 0
	if s.modelCursor >= maxVisible {
		start = s.modelCursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(s.models) {
		end = len(s.models)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	for i := start; i < end; i++ {
		model := s.models[i]
		cursor := "  "
		style := settingsItemStyle
		if i == s.modelCursor {
			cursor = settingsCursorStyle.Render("> ")
			style = settingsSelectedStyle
		}
		suffix := ""
		if model == currentModel {
			suffix = dimStyle.Render(" (current)")
		}
		b.WriteString("  " + cursor + style.Render(model) + suffix + "\n")
	}

	if len(s.models) > maxVisible {
		fmt.Fprintf(&b, "\n  %s", dimStyle.Render(fmt.Sprintf("showing %d-%d of %d", start+1, end, len(s.models))))
	}

	b.WriteString("\n\n")
	hint := "up/down: navigate  enter: select  esc: back"
	if allowDisable {
		hint = "up/down: navigate  enter: select  d: disable reviewer  esc: back"
	}
	b.WriteString("  " + settingsKeyHintStyle.Render(hint))
	return b.String()
}

func fetchModelsCmd(ctx context.Context, listFn func(ctx context.Context) ([]string, error)) tea.Cmd {
	return func() tea.Msg {
		models, err := listFn(ctx)
		return modelsLoadedMsg{models: models, err: err}
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
