package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// HeaderView renders the top header bar showing the logo and persistent status.
func HeaderView(mode Mode, model string, tokenTotal int, width int) string {
	logo := logoStyle.Render("goder")

	var modeLabel string
	switch mode {
	case PlanMode:
		modeLabel = modePlanStyle.Render("PLAN")
	case BuildMode:
		modeLabel = modeBuildStyle.Render("BUILD")
	}

	printer := message.NewPrinter(language.English)
	modelLabel := fmt.Sprintf("%s %s", statusKeyStyle.Render("model:"), statusDescStyle.Render(model))
	tokensLabel := fmt.Sprintf("%s %s", statusKeyStyle.Render("tokens:"), statusDescStyle.Render(printer.Sprintf("%d", tokenTotal)))
	right := fmt.Sprintf("%s  %s", modelLabel, tokensLabel)

	left := fmt.Sprintf("%s  %s", logo, modeLabel)
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}

	bar := fmt.Sprintf("%s%*s%s", left, gap, "", right)
	return headerStyle.Width(width).Render(bar)
}
