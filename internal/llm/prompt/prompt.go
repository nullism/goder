package prompt

import (
	"embed"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/webgovernor/goder/internal/tools"
)

//go:embed prompts/*.md
var promptFS embed.FS

// corePrompt returns the prompt text for the given model, falling back to default.md
// if no model-specific prompt file exists.
func corePrompt(model string) string {
	if model != "" {
		data, err := promptFS.ReadFile("prompts/" + model + ".md")
		if err == nil {
			return string(data)
		}
	}

	data, err := promptFS.ReadFile("prompts/default.md")
	if err != nil {
		// This should never happen since default.md is embedded at compile time.
		panic("prompt: embedded default.md not found: " + err.Error())
	}
	return string(data)
}

// BuildSystemPrompt assembles the full system prompt for the coding agent.
func BuildSystemPrompt(mode string, model string, workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(corePrompt(model))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	sb.WriteString(fmt.Sprintf("- Working directory: %s\n", workDir))
	sb.WriteString(fmt.Sprintf("- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	sb.WriteString(fmt.Sprintf("- Date: %s\n", time.Now().Format("Mon Jan 2 2006")))
	sb.WriteString(fmt.Sprintf("- Mode: %s\n", mode))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project you are helping the user with. ")
	sb.WriteString("All user requests should be interpreted in the context of this directory. ")
	sb.WriteString("When using tools, default to operating within this directory. ")
	sb.WriteString("Use relative paths when referring to files in the project.\n\n")

	// Mode-specific instructions
	if mode == "plan" {
		sb.WriteString("# Mode: PLAN\n\n")
		sb.WriteString("You are in PLAN mode. You should analyze and reason about the codebase but NOT make any modifications.\n")
		sb.WriteString("- Do NOT use tools that modify files (write, edit). These tools are not available in this mode.\n")
		sb.WriteString("- You MUST use the read-only tools (glob, grep, view, ls) to explore the codebase BEFORE answering any question about it. Do not rely on general knowledge alone.\n")
		sb.WriteString("- Your responses MUST reference specific files, functions, types, and patterns found in this codebase. Never give generic advice when project-specific guidance is possible.\n")
		sb.WriteString("- When the user asks how to do something, find existing examples in the codebase first, then base your plan on those concrete patterns.\n")
		sb.WriteString("- Good planning requires investigation. Before forming a plan, search for relevant files, read their contents, and understand the existing code structure.\n")
		sb.WriteString("- When the user asks about changes, explore the codebase first, then explain what changes you would make and where, referencing specific file paths and line numbers.\n")
		sb.WriteString("- If the user wants to execute changes, remind them to switch to BUILD mode (ctrl+t).\n\n")
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
