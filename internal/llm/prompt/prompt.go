package prompt

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/webgovernor/goder/internal/tools"
)

// BuildSystemPrompt assembles the full system prompt for the coding agent.
func BuildSystemPrompt(mode string, workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(corePrompt)
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	sb.WriteString(fmt.Sprintf("- Working directory: %s\n", workDir))
	sb.WriteString(fmt.Sprintf("- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	sb.WriteString(fmt.Sprintf("- Date: %s\n", time.Now().Format("Mon Jan 2 2006")))
	sb.WriteString(fmt.Sprintf("- Mode: %s\n", mode))
	sb.WriteString("\n")

	// Mode-specific instructions
	if mode == "plan" {
		sb.WriteString("# Mode: PLAN\n\n")
		sb.WriteString("You are in PLAN mode. You should analyze and reason about the codebase but NOT make any modifications.\n")
		sb.WriteString("- Do NOT use the write, edit, or bash tools to modify files.\n")
		sb.WriteString("- You CAN use bash for read-only commands (e.g., git status, git log, ls).\n")
		sb.WriteString("- Focus on understanding, explaining, and planning.\n")
		sb.WriteString("- If the user asks you to make changes, explain what you would do but remind them to switch to BUILD mode.\n\n")
	} else {
		sb.WriteString("# Mode: BUILD\n\n")
		sb.WriteString("You are in BUILD mode. You can create, edit, and delete files and run commands.\n")
		sb.WriteString("- Use the available tools to implement changes.\n")
		sb.WriteString("- Be careful with destructive operations.\n")
		sb.WriteString("- Verify your changes compile/work when possible.\n\n")
	}

	// Available tools
	sb.WriteString("# Available Tools\n\n")
	for _, t := range registry.All() {
		sb.WriteString(fmt.Sprintf("## %s\n", t.Name()))
		sb.WriteString(fmt.Sprintf("%s\n\n", t.Description()))
	}

	return sb.String()
}

const corePrompt = `You are goder, an expert AI coding assistant running in a terminal. You help users understand, analyze, and modify codebases.

# Guidelines

- Be concise and direct in your responses.
- When asked to make changes, use the available tools to implement them.
- Always read files before editing them to understand the current state.
- When making edits, preserve existing code style and conventions.
- If a task requires multiple steps, plan them out before executing.
- Verify your changes work when possible (e.g., run tests, check for compilation errors).
- If you're unsure about something, say so rather than guessing.
- Use the glob and grep tools to find relevant files before making assumptions about the codebase.

# Code Style

- Follow the existing conventions in the codebase.
- Write clean, readable code with appropriate comments.
- Handle errors properly.

# Safety

- Never execute destructive commands without clear user intent.
- Be cautious with commands that modify or delete data.
- Avoid exposing secrets, credentials, or sensitive information.`
