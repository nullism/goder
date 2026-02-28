package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// HeaderView renders the top header bar showing the logo, mode indicator, and model name.
func HeaderView(mode Mode, model string, width int) string {
	logo := logoStyle.Render("goder")

	var modeLabel string
	switch mode {
	case PlanMode:
		modeLabel = modePlanStyle.Render("PLAN")
	case BuildMode:
		modeLabel = modeBuildStyle.Render("BUILD")
	}

	modelLabel := dimStyle.Render(model)

	left := fmt.Sprintf("%s  %s  %s", logo, modeLabel, modelLabel)
	right := dimStyle.Render("ctrl+k: settings  ctrl+t: switch mode")

	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}

	bar := fmt.Sprintf("%s%*s%s", left, gap, "", right)
	return headerStyle.Width(width).Render(bar)
}
