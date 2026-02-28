package tui

import "github.com/charmbracelet/lipgloss"

// Color palette
var (
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#06B6D4") // cyan
	colorPlan      = lipgloss.Color("#7C3AED") // violet
	colorBuild     = lipgloss.Color("#06B6D4") // cyan
	colorDim       = lipgloss.Color("#6B7280") // gray
	colorText      = lipgloss.Color("#E5E7EB") // light gray
	colorBg        = lipgloss.Color("#111827") // dark bg
	colorBorder    = lipgloss.Color("#374151") // border gray
	colorError     = lipgloss.Color("#EF4444") // red
	colorUser      = lipgloss.Color("#60A5FA") // blue
	colorAssistant = lipgloss.Color("#A78BFA") // purple
	colorTool      = lipgloss.Color("#FBBF24") // yellow
	colorSuccess   = lipgloss.Color("#34D399") // green
	colorWarning   = lipgloss.Color("#F97316") // orange
)

// Header styles
var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText).
			Padding(0, 1)

	logoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary)

	modePlanStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(colorPlan).
			Padding(0, 1)

	modeBuildStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#000000")).
			Background(colorBuild).
			Padding(0, 1)
)

// Message styles
var (
	userMsgStyle = lipgloss.NewStyle().
			Foreground(colorUser).
			Bold(true)

	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(colorAssistant).
				Bold(true)

	msgContentStyle = lipgloss.NewStyle().
			Foreground(colorText).
			PaddingLeft(2)

	timestampStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	streamingIndicator = lipgloss.NewStyle().
				Foreground(colorSecondary).
				Bold(true)
)

// Tool styles
var (
	toolCallStyle = lipgloss.NewStyle().
			Foreground(colorTool).
			Bold(true)

	toolResultStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	toolErrorStyle = lipgloss.NewStyle().
			Foreground(colorError)
)

// Input area styles
var (
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

	inputFocusedBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorPrimary).
				Padding(0, 1)

	inputPromptStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)
)

// Status bar styles
var (
	statusBarStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Padding(0, 1)

	statusKeyStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Bold(true)

	statusDescStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	statusSepStyle = lipgloss.NewStyle().
			Foreground(colorBorder)

	thinkingStatusStyle = lipgloss.NewStyle().
				Foreground(colorWarning).
				Bold(true)
)

// General styles
var (
	dimStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	errorStyle = lipgloss.NewStyle().
			Foreground(colorError).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	thinkingStyle = lipgloss.NewStyle().
			Foreground(colorWarning).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorWarning).
			Padding(0, 1)

	permissionStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorWarning).
			Padding(0, 1)
)

// Settings overlay styles
var (
	settingsStyle = lipgloss.NewStyle().
			Foreground(colorText).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1)

	settingsTitleStyle = lipgloss.NewStyle().
				Foreground(colorSecondary).
				Bold(true)

	settingsItemStyle = lipgloss.NewStyle().
				Foreground(colorText)

	settingsSelectedStyle = lipgloss.NewStyle().
				Foreground(colorPrimary).
				Bold(true)

	settingsCursorStyle = lipgloss.NewStyle().
				Foreground(colorSecondary).
				Bold(true)

	settingsKeyHintStyle = lipgloss.NewStyle().
				Foreground(colorDim).
				Italic(true)

	settingsSuccessStyle = lipgloss.NewStyle().
				Foreground(colorSuccess).
				Bold(true)

	settingsErrorStyle = lipgloss.NewStyle().
				Foreground(colorError).
				Bold(true)
)
