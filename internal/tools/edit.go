package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EditTool performs find-and-replace edits on files.
type EditTool struct {
	workDir string
}

// NewEditTool creates a new edit tool.
func NewEditTool(workDir string) *EditTool {
	return &EditTool{workDir: workDir}
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Perform exact string replacements in files. The oldString must match exactly (including whitespace and indentation). Use replaceAll to replace all occurrences."
}

func (t *EditTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to edit (absolute or relative to working directory).",
			},
			"old_string": {
				Type:        "string",
				Description: "The exact text to find and replace.",
			},
			"new_string": {
				Type:        "string",
				Description: "The text to replace it with.",
			},
			"replace_all": {
				Type:        "boolean",
				Description: "If true, replace all occurrences. Default is false (replace first occurrence only).",
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *EditTool) RequiresPermission() bool { return true }

func (t *EditTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing edit parameters: %w", err)
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(t.workDir, filePath)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	original := string(content)

	if !strings.Contains(original, params.OldString) {
		return "", fmt.Errorf("oldString not found in %s", params.FilePath)
	}

	var newContent string
	if params.ReplaceAll {
		newContent = strings.ReplaceAll(original, params.OldString, params.NewString)
	} else {
		// Check for multiple matches when not using replace_all
		count := strings.Count(original, params.OldString)
		if count > 1 {
			return "", fmt.Errorf("found %d matches for oldString in %s. Use replace_all=true to replace all, or provide more context to make the match unique", count, params.FilePath)
		}
		newContent = strings.Replace(original, params.OldString, params.NewString, 1)
	}

	if newContent == original {
		return "No changes made (old_string equals new_string).", nil
	}

	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	relPath, _ := filepath.Rel(t.workDir, filePath)
	return fmt.Sprintf("Successfully edited %s", relPath), nil
}
