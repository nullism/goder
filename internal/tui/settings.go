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
	settingsViewMenu            settingsView = iota // main menu
	settingsViewProviders                           // providers list
	settingsViewProviderMenu                        // provider submenu
	settingsViewAPIKey                              // API key input
	settingsViewMaxIter                             // max iterations input
	settingsViewCopilotAuth                         // GitHub Copilot device flow
	settingsViewAgents                              // agents menu (main + planners)
	settingsViewAgentMain                           // main agent provider selection
	settingsViewAgentMainModels                     // main agent model selection
	settingsViewPlanners                            // planners list
	settingsViewPlannerAdd                          // add planner: provider selection
	settingsViewPlannerModels                       // add planner: model selection
)

// Settings holds the state for the settings overlay.
type Settings struct {
	view     settingsView
	apiInput textinput.Model

	// Provider selection state
	providers         []string
	providerCursor    int
	selectedProvider  string
	providerForModels string // provider context used when entering models view

	// Max iterations input
	maxIterInput textinput.Model

	// Model selection state
	models       []string // available models from API
	modelCursor  int      // currently highlighted index
	modelsErr    error    // error from fetching models
	loadingModel bool     // true while fetching models

	// Feedback messages
	feedback    string // success/error message to show
	feedbackErr bool   // true if feedback is an error

	// Copilot device flow state
	copilotUserCode string             // code to display to the user
	copilotURL      string             // verification URL
	copilotPolling  bool               // true while polling for authorization
	copilotCancel   context.CancelFunc // cancels the polling goroutine
	copilotErr      error              // error from the device flow

	// Agent configuration state
	agentProviderCursor int            // cursor for agent provider selection lists
	agentProviderPick   string         // provider chosen when adding/editing an agent
	planners            []agentEntry   // local copy of configured planners
	plannerCursor       int            // cursor within planners list
	modelSelectTarget   modelSelectFor // what the model list is being used for
}

// agentEntry is a provider+model pair displayed in the planners list.
type agentEntry struct {
	Provider string
	Model    string
}

// modelSelectFor distinguishes which flow triggered the model selection list.
type modelSelectFor int

const (
	modelSelectAgentMain  modelSelectFor = iota // main agent model
	modelSelectPlannerAdd                       // adding a new planner
)

// NewSettings creates a new settings component.
// providers is the list of supported provider identifiers;
// currentProvider is the one currently active.
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

	return Settings{
		view:             settingsViewMenu,
		apiInput:         ti,
		maxIterInput:     mi,
		providers:        providers,
		selectedProvider: currentProvider,
	}
}

// --- Async message types for settings ---

// modelsLoadedMsg carries the result of fetching models from the API.
type modelsLoadedMsg struct {
	models []string
	err    error
}

// copilotAuthMsg carries the result of the GitHub Copilot device flow.
type copilotAuthMsg struct {
	token string // OAuth access token on success
	err   error  // error if the flow failed
}

// copilotDeviceCodeMsg carries the device code info to display to the user.
type copilotDeviceCodeMsg struct {
	userCode   string
	url        string
	deviceCode string // needed by model.go to start polling
	interval   int    // poll interval in seconds
	err        error
}

// Update handles key events in the settings overlay.
// Returns the updated settings, whether the overlay should close,
// and any tea.Cmd to execute.
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
	case settingsViewCopilotAuth:
		return s.updateCopilotAuth(msg)
	case settingsViewAgents:
		return s.updateAgents(msg)
	case settingsViewAgentMain:
		return s.updateAgentMain(msg)
	case settingsViewAgentMainModels:
		return s.updateAgentMainModels(msg)
	case settingsViewPlanners:
		return s.updatePlanners(msg)
	case settingsViewPlannerAdd:
		return s.updatePlannerAdd(msg)
	case settingsViewPlannerModels:
		return s.updatePlannerModels(msg)
	}
	return s, false, nil
}

// updateMenu handles keys in the main settings menu.
func (s Settings) updateMenu(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+k":
		return s, true, nil // close settings
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
	case "3", "a", "A":
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
		// For copilot, use the device flow instead of a text input.
		if s.selectedProvider == "copilot" {
			s.view = settingsViewCopilotAuth
			s.feedback = ""
			s.copilotUserCode = ""
			s.copilotURL = ""
			s.copilotPolling = false
			s.copilotErr = nil
			return s, false, nil // model.go will trigger the device code request
		}
		s.view = settingsViewAPIKey
		s.feedback = ""
		s.apiInput.SetValue("")
		s.apiInput.Focus()
		return s, false, s.apiInput.Cursor.BlinkCmd()
	}
	return s, false, nil
}

// updateAPIKey handles keys in the API key input sub-view.
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
		// Signal to model.go to save the key
		s.apiInput.Blur()
		return s, false, nil // actual save handled by model.go checking for enter
	}

	// Forward to text input
	var cmd tea.Cmd
	s.apiInput, cmd = s.apiInput.Update(msg)
	return s, false, cmd
}

// updateMaxIter handles keys in the max iterations input sub-view.
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
		// Signal to model.go to save the value
		s.maxIterInput.Blur()
		return s, false, nil // actual save handled by model.go checking for enter
	}

	// Only allow digit keys in the text input
	if len(msg.String()) == 1 && msg.String()[0] >= '0' && msg.String()[0] <= '9' {
		var cmd tea.Cmd
		s.maxIterInput, cmd = s.maxIterInput.Update(msg)
		return s, false, cmd
	}

	// Allow backspace/delete
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		var cmd tea.Cmd
		s.maxIterInput, cmd = s.maxIterInput.Update(msg)
		return s, false, cmd
	}

	return s, false, nil
}

// MaxIterValue returns the current value in the max iterations input as an int, or 0 if invalid.
func (s Settings) MaxIterValue() int {
	val := strings.TrimSpace(s.maxIterInput.Value())
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// HandleModelsLoaded processes the modelsLoadedMsg.
func (s *Settings) HandleModelsLoaded(models []string, err error) {
	s.loadingModel = false
	if err != nil {
		s.modelsErr = err
		return
	}
	s.models = models
	s.modelCursor = 0
}

// SetFeedback sets a feedback message on the settings overlay.
func (s *Settings) SetFeedback(msg string, isErr bool) {
	s.feedback = msg
	s.feedbackErr = isErr
}

// SetView updates the active settings sub-view.
func (s *Settings) SetView(view settingsView) {
	s.view = view
}

// SelectedProvider returns the currently selected provider identifier.
func (s Settings) SelectedProvider() string {
	return s.selectedProvider
}

// SelectedModel returns the currently highlighted model ID, or empty if none.
func (s Settings) SelectedModel() string {
	if len(s.models) > 0 && s.modelCursor < len(s.models) {
		return s.models[s.modelCursor]
	}
	return ""
}

// APIKeyValue returns the current value in the API key input.
func (s Settings) APIKeyValue() string {
	return strings.TrimSpace(s.apiInput.Value())
}

// View renders the settings overlay.
func (s Settings) View(width int, currentKey string, currentMaxIter int, mainAgent agentEntry, planners []agentEntry) string {
	innerWidth := width - 6 // account for border + padding

	var content string
	switch s.view {
	case settingsViewMenu:
		content = s.viewMenu(currentMaxIter, mainAgent, planners)
	case settingsViewProviders:
		content = s.viewProviders()
	case settingsViewProviderMenu:
		content = s.viewProviderMenu(currentKey)
	case settingsViewAPIKey:
		content = s.viewAPIKey(innerWidth)
	case settingsViewMaxIter:
		content = s.viewMaxIter(innerWidth, currentMaxIter)
	case settingsViewCopilotAuth:
		content = s.viewCopilotAuth()
	case settingsViewAgents:
		content = s.viewAgents(mainAgent, planners)
	case settingsViewAgentMain:
		content = s.viewAgentMain(mainAgent)
	case settingsViewAgentMainModels:
		content = s.viewAgentMainModels(mainAgent)
	case settingsViewPlanners:
		content = s.viewPlannersList(planners)
	case settingsViewPlannerAdd:
		content = s.viewPlannerAdd()
	case settingsViewPlannerModels:
		content = s.viewPlannerModels()
	}

	return settingsStyle.Width(innerWidth).Render(content)
}

// viewMenu renders the main settings menu.
func (s Settings) viewMenu(currentMaxIter int, mainAgent agentEntry, planners []agentEntry) string {
	title := settingsTitleStyle.Render("Settings")

	agentSummary := fmt.Sprintf("%s:%s", mainAgent.Provider, mainAgent.Model)
	if len(planners) > 0 {
		agentSummary += fmt.Sprintf(" + %d planners", len(planners))
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  [1] Providers   %s\n", dimStyle.Render(s.selectedProvider))
	fmt.Fprintf(&b, "  [2] Max Iters   %s\n", dimStyle.Render(strconv.Itoa(currentMaxIter)))
	fmt.Fprintf(&b, "  [3] Agents      %s\n", dimStyle.Render(agentSummary))

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

// viewAPIKey renders the API key input sub-view.
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

// viewMaxIter renders the max iterations input sub-view.
func (s Settings) viewMaxIter(width int, currentMaxIter int) string {
	title := settingsTitleStyle.Render("Max Agent Iterations")
	s.maxIterInput.Width = 10

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  Current: %s\n\n", dimStyle.Render(strconv.Itoa(currentMaxIter)))
	b.WriteString("  " + s.maxIterInput.View() + "\n")

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

// updateCopilotAuth handles keys in the Copilot device flow auth view.
func (s Settings) updateCopilotAuth(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		// Cancel polling if in progress.
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

// HandleCopilotDeviceCode processes the copilotDeviceCodeMsg.
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

// HandleCopilotAuth processes the copilotAuthMsg (token result).
func (s *Settings) HandleCopilotAuth(token string, err error) {
	s.copilotPolling = false
	if s.copilotCancel != nil {
		s.copilotCancel()
		s.copilotCancel = nil
	}
	if err != nil {
		s.copilotErr = err
		return
	}
	// Success — model.go handles saving the token.
}

// SetCopilotCancel stores the cancel function for the polling goroutine.
func (s *Settings) SetCopilotCancel(cancel context.CancelFunc) {
	s.copilotCancel = cancel
}

// viewCopilotAuth renders the GitHub Copilot device flow authentication view.
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

// --- Agent settings update methods ---

// updateAgents handles keys in the agents sub-menu.
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
	case "2", "p", "P":
		s.view = settingsViewPlanners
		s.plannerCursor = 0
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

// updateAgentMain handles keys in the main agent provider selection.
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

// updateAgentMainModels handles keys in the main agent model selection.
func (s Settings) updateAgentMainModels(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	if s.loadingModel {
		if msg.String() == "esc" {
			s.view = settingsViewAgentMain
			s.loadingModel = false
			return s, false, nil
		}
		return s, false, nil
	}
	if s.modelsErr != nil {
		if msg.String() == "esc" {
			s.view = settingsViewAgentMain
			s.modelsErr = nil
			return s, false, nil
		}
		return s, false, nil
	}
	switch msg.String() {
	case "esc":
		s.view = settingsViewAgentMain
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
	case "enter":
		// model.go handles the save
		return s, false, nil
	}
	return s, false, nil
}

// updatePlanners handles keys in the planners list view.
func (s Settings) updatePlanners(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewAgents
		return s, false, nil
	case "up", "k":
		if s.plannerCursor > 0 {
			s.plannerCursor--
		}
		return s, false, nil
	case "down", "j":
		if len(s.planners) > 0 && s.plannerCursor < len(s.planners)-1 {
			s.plannerCursor++
		}
		return s, false, nil
	case "a", "A":
		s.view = settingsViewPlannerAdd
		s.agentProviderCursor = 0
		s.feedback = ""
		return s, false, nil
	case "d", "D":
		// Delete highlighted planner — model.go handles persistence
		if len(s.planners) > 0 && s.plannerCursor < len(s.planners) {
			s.planners = append(s.planners[:s.plannerCursor], s.planners[s.plannerCursor+1:]...)
			if s.plannerCursor >= len(s.planners) && s.plannerCursor > 0 {
				s.plannerCursor--
			}
			return s, false, nil
		}
		return s, false, nil
	}
	return s, false, nil
}

// updatePlannerAdd handles keys in the add-planner provider selection.
func (s Settings) updatePlannerAdd(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc":
		s.view = settingsViewPlanners
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
		s.modelSelectTarget = modelSelectPlannerAdd
		s.view = settingsViewPlannerModels
		s.modelCursor = 0
		s.models = nil
		s.modelsErr = nil
		s.loadingModel = true
		s.feedback = ""
		return s, false, nil
	}
	return s, false, nil
}

// updatePlannerModels handles keys in the add-planner model selection.
func (s Settings) updatePlannerModels(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	if s.loadingModel {
		if msg.String() == "esc" {
			s.view = settingsViewPlannerAdd
			s.loadingModel = false
			return s, false, nil
		}
		return s, false, nil
	}
	if s.modelsErr != nil {
		if msg.String() == "esc" {
			s.view = settingsViewPlannerAdd
			s.modelsErr = nil
			return s, false, nil
		}
		return s, false, nil
	}
	switch msg.String() {
	case "esc":
		s.view = settingsViewPlannerAdd
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
	case "enter":
		// model.go handles the save
		return s, false, nil
	}
	return s, false, nil
}

// --- Agent settings view methods ---

// viewAgents renders the agents sub-menu.
func (s Settings) viewAgents(mainAgent agentEntry, planners []agentEntry) string {
	title := settingsTitleStyle.Render("Agents")

	mainSummary := fmt.Sprintf("%s:%s", mainAgent.Provider, mainAgent.Model)
	plannerSummary := "none"
	if len(planners) > 0 {
		plannerSummary = fmt.Sprintf("%d configured", len(planners))
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	fmt.Fprintf(&b, "  [1] Main Agent  %s\n", dimStyle.Render(mainSummary))
	fmt.Fprintf(&b, "  [2] Planners    %s\n", dimStyle.Render(plannerSummary))
	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
	return b.String()
}

// viewAgentMain renders the main agent provider selection list.
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

// viewAgentMainModels renders the model selection for the main agent.
func (s Settings) viewAgentMainModels(mainAgent agentEntry) string {
	title := settingsTitleStyle.Render("Main Agent Model")
	return s.viewModelList(title, mainAgent.Model)
}

// viewPlannersList renders the list of configured planners.
func (s Settings) viewPlannersList(planners []agentEntry) string {
	title := settingsTitleStyle.Render("Planning Agents")

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	if len(planners) == 0 {
		b.WriteString("  " + dimStyle.Render("No planning agents configured") + "\n")
		b.WriteString("  " + dimStyle.Render("Add planners to enable multi-agent planning") + "\n")
	} else {
		for i, p := range planners {
			cursor := "  "
			style := settingsItemStyle
			if i == s.plannerCursor {
				cursor = settingsCursorStyle.Render("> ")
				style = settingsSelectedStyle
			}
			b.WriteString("  " + cursor + style.Render(fmt.Sprintf("%s:%s", p.Provider, p.Model)) + "\n")
		}
	}

	b.WriteString("\n")
	hints := "[a] add  esc: back"
	if len(planners) > 0 {
		hints = "[a] add  [d] delete  esc: back"
	}
	b.WriteString("  " + settingsKeyHintStyle.Render(hints))
	return b.String()
}

// viewPlannerAdd renders the provider selection for adding a planner.
func (s Settings) viewPlannerAdd() string {
	title := settingsTitleStyle.Render("Add Planner — Select Provider")

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	for i, p := range s.providers {
		cursor := "  "
		style := settingsItemStyle
		if i == s.agentProviderCursor {
			cursor = settingsCursorStyle.Render("> ")
			style = settingsSelectedStyle
		}
		b.WriteString("  " + cursor + style.Render(titleCase(p)) + "\n")
	}

	b.WriteString("\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("up/down: navigate  enter: select  esc: back"))
	return b.String()
}

// viewPlannerModels renders the model selection for adding a planner.
func (s Settings) viewPlannerModels() string {
	title := settingsTitleStyle.Render("Add Planner — Select Model")
	return s.viewModelList(title, "")
}

// viewModelList is a shared helper for rendering a scrollable model list.
// It reuses the same models/modelCursor/loadingModel/modelsErr state.
func (s Settings) viewModelList(title, currentModel string) string {
	var b strings.Builder
	b.WriteString("  " + title + "\n\n")

	if s.loadingModel {
		b.WriteString("  Loading models...")
		b.WriteString("\n\n")
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
		fmt.Fprintf(&b, "\n  %s",
			dimStyle.Render(fmt.Sprintf("showing %d-%d of %d", start+1, end, len(s.models))))
	}

	if s.feedback != "" {
		b.WriteString("\n")
		if s.feedbackErr {
			b.WriteString("  " + settingsErrorStyle.Render(s.feedback))
		} else {
			b.WriteString("  " + settingsSuccessStyle.Render(s.feedback))
		}
	}

	b.WriteString("\n\n")
	b.WriteString("  " + settingsKeyHintStyle.Render("up/down: navigate  enter: select  esc: back"))
	return b.String()
}

// Planners returns a copy of the settings' local planners list.
func (s Settings) Planners() []agentEntry {
	out := make([]agentEntry, len(s.planners))
	copy(out, s.planners)
	return out
}

// SetPlanners replaces the settings' local planners list.
func (s *Settings) SetPlanners(planners []agentEntry) {
	s.planners = make([]agentEntry, len(planners))
	copy(s.planners, planners)
}

// AgentProviderPick returns the provider selected during agent configuration.
func (s Settings) AgentProviderPick() string {
	return s.agentProviderPick
}

// ModelSelectTarget returns the current model selection target.
func (s Settings) ModelSelectTarget() modelSelectFor {
	return s.modelSelectTarget
}

// fetchModelsCmd creates a tea.Cmd that fetches models from the provider.
func fetchModelsCmd(ctx context.Context, listFn func(ctx context.Context) ([]string, error)) tea.Cmd {
	return func() tea.Msg {
		models, err := listFn(ctx)
		return modelsLoadedMsg{models: models, err: err}
	}
}

// titleCase returns s with the first letter uppercased.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
