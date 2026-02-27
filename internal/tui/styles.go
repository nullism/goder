package tui

import "github.com/charmbracelet/lipgloss"

// Color palette
var (
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#06B6D4") // cyan
	colorPlan      = lipgloss.Color("#F59E0B") // amber
	colorBuild     = lipgloss.Color("#10B981") // emerald
	colorDim       = lipgloss.Color("#6B7280") // gray
	colorText      = lipgloss.Color("#E5E7EB") // light gray
	colorBg        = lipgloss.Color("#111827") // dark bg
	colorBorder    = lipgloss.Color("#374151") // border gray
	colorError     = lipgloss.Color("#EF4444") // red
	colorUser      = lipgloss.Color("#60A5FA") // blue
	colorAssistant = lipgloss.Color("#A78BFA") // purple
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
)
