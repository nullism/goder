package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LsTool lists directory contents.
type LsTool struct {
	workDir string
}

// NewLsTool creates a new ls tool.
func NewLsTool(workDir string) *LsTool {
	return &LsTool{workDir: workDir}
}

func (t *LsTool) Name() string { return "ls" }

func (t *LsTool) Description() string {
	return "List directory contents. Returns entries one per line with a trailing / for subdirectories."
}

func (t *LsTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"path": {
				Type:        "string",
				Description: "The directory to list. Defaults to the working directory.",
			},
		},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *LsTool) RequiresPermission() bool { return false }

func (t *LsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing ls parameters: %w", err)
	}

	dir := t.workDir
	if params.Path != "" {
		if filepath.IsAbs(params.Path) {
			dir = params.Path
		} else {
			dir = filepath.Join(t.workDir, params.Path)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading directory: %w", err)
	}

	var lines []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}

	if len(lines) == 0 {
		return "(empty directory)", nil
	}

	return strings.Join(lines, "\n"), nil
}
