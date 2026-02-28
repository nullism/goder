package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// WriteTool creates or overwrites files.
type WriteTool struct {
	workDir string
}

// NewWriteTool creates a new write tool.
func NewWriteTool(workDir string) *WriteTool {
	return &WriteTool{workDir: workDir}
}

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Description() string {
	return "Write content to a file, creating it if it doesn't exist or overwriting if it does. Parent directories are created automatically."
}

func (t *WriteTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to write (absolute or relative to working directory).",
			},
			"content": {
				Type:        "string",
				Description: "The content to write to the file.",
			},
		},
		Required: []string{"file_path", "content"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *WriteTool) RequiresPermission() bool { return true }

func (t *WriteTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing write parameters: %w", err)
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(t.workDir, filePath)
	}

	// Create parent directories if needed
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating directories: %w", err)
	}

	if err := os.WriteFile(filePath, []byte(params.Content), 0o644); err != nil {
		return "", fmt.Errorf("writing file: %w", err)
	}

	relPath, _ := filepath.Rel(t.workDir, filePath)
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(params.Content), relPath), nil
}
