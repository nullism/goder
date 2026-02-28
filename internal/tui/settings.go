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
	settingsViewMenu    settingsView = iota // main menu
	settingsViewAPIKey                      // API key input
	settingsViewModels                      // model selection list
	settingsViewMaxIter                     // max iterations input
)

// Settings holds the state for the settings overlay.
type Settings struct {
	view     settingsView
	apiInput textinput.Model

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
}

// NewSettings creates a new settings component.
func NewSettings() Settings {
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
		view:         settingsViewMenu,
		apiInput:     ti,
		maxIterInput: mi,
	}
}

// --- Async message types for settings ---

// modelsLoadedMsg carries the result of fetching models from the API.
type modelsLoadedMsg struct {
	models []string
	err    error
}

// settingsAPIKeySavedMsg signals that the API key was saved successfully.
type settingsAPIKeySavedMsg struct{}

// settingsModelSavedMsg signals that the model was saved successfully.
type settingsModelSavedMsg struct{ model string }

// Update handles key events in the settings overlay.
// Returns the updated settings, whether the overlay should close,
// and any tea.Cmd to execute.
func (s Settings) Update(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch s.view {
	case settingsViewMenu:
		return s.updateMenu(msg)
	case settingsViewAPIKey:
		return s.updateAPIKey(msg)
	case settingsViewModels:
		return s.updateModels(msg)
	case settingsViewMaxIter:
		return s.updateMaxIter(msg)
	}
	return s, false, nil
}

// updateMenu handles keys in the main settings menu.
func (s Settings) updateMenu(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+k":
		return s, true, nil // close settings
	case "1", "a", "A":
		s.view = settingsViewAPIKey
		s.feedback = ""
		s.apiInput.SetValue("")
		s.apiInput.Focus()
		return s, false, s.apiInput.Cursor.BlinkCmd()
	case "2", "m", "M":
		s.view = settingsViewModels
		s.feedback = ""
		s.modelCursor = 0
		s.models = nil
		s.modelsErr = nil
		s.loadingModel = true
		return s, false, nil // model fetch is triggered from model.go
	case "3", "i", "I":
		s.view = settingsViewMaxIter
		s.feedback = ""
		s.maxIterInput.SetValue("")
		s.maxIterInput.Focus()
		return s, false, s.maxIterInput.Cursor.BlinkCmd()
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

// updateModels handles keys in the model selection sub-view.
func (s Settings) updateModels(msg tea.KeyMsg) (Settings, bool, tea.Cmd) {
	if s.loadingModel {
		// Only allow esc while loading
		if msg.String() == "esc" {
			s.view = settingsViewMenu
			s.loadingModel = false
			return s, false, nil
		}
		return s, false, nil
	}

	if s.modelsErr != nil {
		// Only allow esc on error
		if msg.String() == "esc" {
			s.view = settingsViewMenu
			s.modelsErr = nil
			return s, false, nil
		}
		return s, false, nil
	}

	switch msg.String() {
	case "esc":
		s.view = settingsViewMenu
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
		if len(s.models) > 0 && s.modelCursor < len(s.models) {
			// Signal to model.go to save the selected model
			return s, false, nil // actual save handled by model.go
		}
		return s, false, nil
	}
	return s, false, nil
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
func (s Settings) View(width int, currentKey, currentModel string, currentMaxIter int) string {
	innerWidth := width - 6 // account for border + padding

	var content string
	switch s.view {
	case settingsViewMenu:
		content = s.viewMenu(currentKey, currentModel, currentMaxIter)
	case settingsViewAPIKey:
		content = s.viewAPIKey(innerWidth)
	case settingsViewModels:
		content = s.viewModels(currentModel)
	case settingsViewMaxIter:
		content = s.viewMaxIter(innerWidth, currentMaxIter)
	}

	return settingsStyle.Width(innerWidth).Render(content)
}

// viewMenu renders the main settings menu.
func (s Settings) viewMenu(currentKey, currentModel string, currentMaxIter int) string {
	title := settingsTitleStyle.Render("Settings")

	maskedKey := "(not set)"
	if currentKey != "" {
		if len(currentKey) > 8 {
			maskedKey = currentKey[:3] + "..." + currentKey[len(currentKey)-4:]
		} else {
			maskedKey = "****"
		}
	}

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	b.WriteString(fmt.Sprintf("  [1] API Key     %s\n", dimStyle.Render(maskedKey)))
	b.WriteString(fmt.Sprintf("  [2] Model       %s\n", dimStyle.Render(currentModel)))
	b.WriteString(fmt.Sprintf("  [3] Max Iters   %s\n", dimStyle.Render(strconv.Itoa(currentMaxIter))))

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

// viewAPIKey renders the API key input sub-view.
func (s Settings) viewAPIKey(width int) string {
	title := settingsTitleStyle.Render("Enter OpenAI API Key")
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

// viewModels renders the model selection list sub-view.
func (s Settings) viewModels(currentModel string) string {
	title := settingsTitleStyle.Render("Select Model")

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
		
  b.WriteString("  OpenAI\n\n")
		b.WriteString("\n\n")
		b.WriteString("  " + settingsKeyHintStyle.Render("esc: back"))
		return b.String()
	}

	  b.WriteString("  OpenAI\n\n")

	maxVisible := 10
	if maxVisible > len(s.models) {
		maxVisible = len(s.models)
	}

	// Calculate scroll window
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
		b.WriteString(fmt.Sprintf("\n  %s",
			dimStyle.Render(fmt.Sprintf("showing %d-%d of %d", start+1, end, len(s.models)))))
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

// viewMaxIter renders the max iterations input sub-view.
func (s Settings) viewMaxIter(width int, currentMaxIter int) string {
	title := settingsTitleStyle.Render("Max Agent Iterations")
	s.maxIterInput.Width = 10

	var b strings.Builder
	b.WriteString("  " + title + "\n\n")
	b.WriteString(fmt.Sprintf("  Current: %s\n\n", dimStyle.Render(strconv.Itoa(currentMaxIter))))
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

// fetchModelsCmd creates a tea.Cmd that fetches models from the provider.
func fetchModelsCmd(ctx context.Context, listFn func(ctx context.Context) ([]string, error)) tea.Cmd {
	return func() tea.Msg {
		models, err := listFn(ctx)
		return modelsLoadedMsg{models: models, err: err}
	}
}
