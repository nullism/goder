package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ViewTool reads file contents with optional offset and limit.
type ViewTool struct {
	workDir string
}

// NewViewTool creates a new view tool.
func NewViewTool(workDir string) *ViewTool {
	return &ViewTool{workDir: workDir}
}

func (t *ViewTool) Name() string { return "view" }

func (t *ViewTool) Description() string {
	return "Read a file's contents. Returns lines prefixed with line numbers. Use offset and limit to read specific sections of large files."
}

func (t *ViewTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"file_path": {
				Type:        "string",
				Description: "The path to the file to read (absolute or relative to working directory).",
			},
			"offset": {
				Type:        "number",
				Description: "The line number to start reading from (1-indexed). Defaults to 1.",
			},
			"limit": {
				Type:        "number",
				Description: "The maximum number of lines to read. Defaults to 2000.",
			},
		},
		Required: []string{"file_path"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *ViewTool) RequiresPermission() bool { return false }

func (t *ViewTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing view parameters: %w", err)
	}

	if params.Offset <= 0 {
		params.Offset = 1
	}
	if params.Limit <= 0 {
		params.Limit = 2000
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(t.workDir, filePath)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	// Increase buffer size for long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < params.Offset {
			continue
		}
		if lineNum >= params.Offset+params.Limit {
			break
		}

		line := scanner.Text()
		// Truncate very long lines
		if len(line) > 2000 {
			line = line[:2000] + "... (truncated)"
		}
		lines = append(lines, fmt.Sprintf("%d: %s", lineNum, line))
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}

	if len(lines) == 0 {
		return "(empty file or offset beyond end of file)", nil
	}

	return strings.Join(lines, "\n"), nil
}
