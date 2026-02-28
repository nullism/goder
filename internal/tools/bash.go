package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// BashTool executes shell commands.
type BashTool struct {
	workDir string
}

// NewBashTool creates a new bash tool.
func NewBashTool(workDir string) *BashTool {
	return &BashTool{workDir: workDir}
}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Description() string {
	return "Execute a bash command in the working directory. Returns stdout and stderr. Use this for running builds, tests, git commands, and other terminal operations."
}

func (t *BashTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"command": {
				Type:        "string",
				Description: "The bash command to execute.",
			},
			"timeout": {
				Type:        "number",
				Description: "Optional timeout in seconds. Defaults to 120.",
			},
		},
		Required: []string{"command"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *BashTool) RequiresPermission() bool { return true }

func (t *BashTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing bash parameters: %w", err)
	}

	if params.Timeout <= 0 {
		params.Timeout = 120
	}

	timeout := time.Duration(params.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", params.Command)
	cmd.Dir = t.workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("STDERR:\n")
		result.WriteString(stderr.String())
	}

	output := result.String()

	// Truncate very long output
	const maxOutput = 50000
	if len(output) > maxOutput {
		output = output[:maxOutput] + "\n... (output truncated)"
	}

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return output + "\n(command timed out)", fmt.Errorf("command timed out after %ds", params.Timeout)
		}
		if output == "" {
			return "", fmt.Errorf("command failed: %w", err)
		}
		// Include the output even on error (exit code != 0)
		return output + fmt.Sprintf("\n(exit code: %s)", err), nil
	}

	if output == "" {
		return "(no output)", nil
	}

	return output, nil
}
