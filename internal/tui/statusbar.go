package tui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// StatusBarView renders the bottom status bar.
func StatusBarView(width int, thinking bool) string {
	sep := statusSepStyle.Render(" | ")

	items := []string{}
	if thinking {
		items = append(items, thinkingStatusStyle.Render("thinking..."))
	}

	items = append(items,
		fmt.Sprintf("%s %s", statusKeyStyle.Render("ctrl+s"), statusDescStyle.Render("submit")),
		fmt.Sprintf("%s %s", statusKeyStyle.Render("ctrl+t"), statusDescStyle.Render("toggle")),
		fmt.Sprintf("%s %s", statusKeyStyle.Render("ctrl+k"), statusDescStyle.Render("settings")),
		fmt.Sprintf("%s %s", statusKeyStyle.Render("esc"), statusDescStyle.Render("cancel")),
		fmt.Sprintf("%s %s", statusKeyStyle.Render("ctrl+c"), statusDescStyle.Render("quit")),
	)

	bar := ""
	for i, item := range items {
		if i > 0 {
			bar += sep
		}
		bar += item
	}

	return statusBarStyle.Width(width).
		Align(lipgloss.Center).
		Render(bar)
}
