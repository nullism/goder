package prompt

import (
	"embed"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/nullism/goder/internal/tools"
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

// namedPrompt returns the prompt text for a named embedded prompt file.
func namedPrompt(name string) string {
	data, err := promptFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		panic("prompt: embedded " + name + ".md not found: " + err.Error())
	}
	return string(data)
}

// BuildSystemPrompt assembles the full system prompt for the coding agent.
func BuildSystemPrompt(model string, workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(corePrompt(model))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", workDir)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("Mon Jan 2 2006"))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project you are helping the user with. ")
	sb.WriteString("All user requests should be interpreted in the context of this directory. ")
	sb.WriteString("When using tools, default to operating within this directory. ")
	sb.WriteString("Use relative paths when referring to files in the project.\n\n")

	// Tool usage instructions
	sb.WriteString("You can create, edit, and delete files and run commands using the available tools.\n")
	sb.WriteString("- Use the available tools to implement changes.\n")
	sb.WriteString("- Be careful with destructive operations.\n")
	sb.WriteString("- Verify your changes compile/work when possible.\n\n")

	// Available tools
	sb.WriteString("# Available Tools\n\n")
	for _, t := range registry.All() {
		fmt.Fprintf(&sb, "## %s\n", t.Name())
		fmt.Fprintf(&sb, "%s\n\n", t.Description())
	}

	return sb.String()
}

// BuildPlanDraftPrompt assembles the system prompt for the main planning pass.
func BuildPlanDraftPrompt(workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(namedPrompt("plan_draft"))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", workDir)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("Mon Jan 2 2006"))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project. ")
	sb.WriteString("Use relative paths when referring to files.\n\n")

	// Available tools (read-only only — planning is plan-only)
	sb.WriteString("# Available Tools\n\n")
	for _, t := range registry.All() {
		if t.RequiresPermission() {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n", t.Name())
		fmt.Fprintf(&sb, "%s\n\n", t.Description())
	}

	return sb.String()
}

// BuildPlanReviewPrompt assembles the system prompt for the review pass.
func BuildPlanReviewPrompt(workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(namedPrompt("reviewer"))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", workDir)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("Mon Jan 2 2006"))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project. ")
	sb.WriteString("Use relative paths when referring to files.\n\n")

	// Available tools (read-only only — reviewer can inspect but not modify)
	sb.WriteString("# Available Tools\n\n")
	for _, t := range registry.All() {
		if t.RequiresPermission() {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n", t.Name())
		fmt.Fprintf(&sb, "%s\n\n", t.Description())
	}

	return sb.String()
}

// BuildPlanSummaryPrompt assembles the system prompt for final plan summary.
func BuildPlanSummaryPrompt(workDir string) string {
	var sb strings.Builder

	sb.WriteString(namedPrompt("plan_summary"))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", workDir)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("Mon Jan 2 2006"))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project. ")
	sb.WriteString("Use relative paths when referring to files.\n\n")

	return sb.String()
}

// BuildOrchestratorPrompt assembles the system prompt for the main orchestrator.
func BuildOrchestratorPrompt(workDir string, registry *tools.Registry) string {
	var sb strings.Builder

	sb.WriteString(namedPrompt("orchestrator"))
	sb.WriteString("\n\n")

	// Environment info
	sb.WriteString("# Environment\n\n")
	fmt.Fprintf(&sb, "- Working directory: %s\n", workDir)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("Mon Jan 2 2006"))
	sb.WriteString("\n")
	sb.WriteString("The working directory is the root of the project. ")
	sb.WriteString("Use relative paths when referring to files.\n\n")

	// Available tools (read-only + orchestration tools only)
	sb.WriteString("# Available Tools\n\n")
	for _, t := range registry.All() {
		if t.RequiresPermission() {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n", t.Name())
		fmt.Fprintf(&sb, "%s\n\n", t.Description())
	}

	return sb.String()
}
