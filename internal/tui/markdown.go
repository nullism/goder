package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

var markdownRenderer = newMarkdownRenderer()

func newMarkdownRenderer() *glamour.TermRenderer {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithEnvironmentConfig(),
		glamour.WithWordWrap(0),
	)
	if err != nil {
		return nil
	}
	return renderer
}

func renderMarkdown(content string) string {
	if strings.TrimSpace(content) == "" {
		return content
	}
	if markdownRenderer == nil {
		return content
	}
	rendered, err := markdownRenderer.Render(content)
	if err != nil {
		return content
	}
	return strings.TrimSuffix(rendered, "\n")
}
