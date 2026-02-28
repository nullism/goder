package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GlobTool finds files matching a glob pattern.
type GlobTool struct {
	workDir string
}

// NewGlobTool creates a new glob tool.
func NewGlobTool(workDir string) *GlobTool {
	return &GlobTool{workDir: workDir}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Fast file pattern matching tool. Supports glob patterns like \"**/*.go\" or \"src/**/*.ts\". Returns matching file paths sorted by name."
}

func (t *GlobTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"pattern": {
				Type:        "string",
				Description: "The glob pattern to match files against (e.g. \"**/*.go\", \"internal/**/*.go\")",
			},
			"path": {
				Type:        "string",
				Description: "The directory to search in. Defaults to the working directory.",
			},
		},
		Required: []string{"pattern"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *GlobTool) RequiresPermission() bool { return false }

func (t *GlobTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing glob parameters: %w", err)
	}

	baseDir := t.workDir
	if params.Path != "" {
		if filepath.IsAbs(params.Path) {
			baseDir = params.Path
		} else {
			baseDir = filepath.Join(t.workDir, params.Path)
		}
	}

	pattern := filepath.Join(baseDir, params.Pattern)
	matches, err := doublestar.FilepathGlob(pattern)
	if err != nil {
		return "", fmt.Errorf("glob error: %w", err)
	}

	if len(matches) == 0 {
		return "No files matched the pattern.", nil
	}

	// Make paths relative to workDir for cleaner output
	var relative []string
	for _, m := range matches {
		rel, err := filepath.Rel(t.workDir, m)
		if err != nil {
			rel = m
		}
		relative = append(relative, rel)
	}

	return strings.Join(relative, "\n"), nil
}
