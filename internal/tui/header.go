package tui

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/nullism/goder/internal/version"
)

// HeaderView renders the top header bar showing the logo and persistent status.
func HeaderView(model string, tokenTotal int, perModel map[string]int, width int) string {
	logo := logoStyle.Render("goder") + " " + dimStyle.Render(version.Version)

	printer := message.NewPrinter(language.English)
	modelLabel := fmt.Sprintf("%s %s", statusKeyStyle.Render("model:"), statusDescStyle.Render(model))
	tokensLabel := fmt.Sprintf("%s %s", statusKeyStyle.Render("tokens:"), statusDescStyle.Render(printer.Sprintf("%d", tokenTotal)))

	// Build per-model breakdown if there's data for more than one model.
	perModelLabel := formatPerModelTokens(perModel, printer)

	right := fmt.Sprintf("%s  %s", modelLabel, tokensLabel)
	if perModelLabel != "" {
		right += "  " + perModelLabel
	}

	left := logo
	gap := width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}

	bar := fmt.Sprintf("%s%*s%s", left, gap, "", right)
	return headerStyle.Width(width).Render(bar)
}

// formatPerModelTokens produces a compact per-model breakdown string.
// Returns "" if the map has fewer than 2 entries (the overall total
// already conveys enough information for a single model).
func formatPerModelTokens(perModel map[string]int, printer *message.Printer) string {
	if len(perModel) < 2 {
		return ""
	}

	// Sort models alphabetically for stable output.
	models := make([]string, 0, len(perModel))
	for m := range perModel {
		models = append(models, m)
	}
	sort.Strings(models)

	var parts []string
	for _, m := range models {
		tok := perModel[m]
		if tok == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s",
			dimStyle.Render(m),
			dimStyle.Render(printer.Sprintf("%d", tok)),
		))
	}

	if len(parts) == 0 {
		return ""
	}

	// Join with separator
	result := statusKeyStyle.Render("[")
	for i, p := range parts {
		if i > 0 {
			result += dimStyle.Render(" | ")
		}
		result += p
	}
	result += statusKeyStyle.Render("]")
	return result
}
