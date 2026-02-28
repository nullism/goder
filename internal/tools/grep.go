package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// GrepTool searches file contents using regular expressions.
type GrepTool struct {
	workDir string
}

// NewGrepTool creates a new grep tool.
func NewGrepTool(workDir string) *GrepTool {
	return &GrepTool{workDir: workDir}
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "Fast content search tool. Searches file contents using regular expressions. Returns file paths and line numbers with matching content."
}

func (t *GrepTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"pattern": {
				Type:        "string",
				Description: "The regex pattern to search for in file contents.",
			},
			"path": {
				Type:        "string",
				Description: "The directory to search in. Defaults to the working directory.",
			},
			"include": {
				Type:        "string",
				Description: "File pattern to include in the search (e.g. \"*.go\", \"*.{ts,tsx}\").",
			},
		},
		Required: []string{"pattern"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *GrepTool) RequiresPermission() bool { return false }

func (t *GrepTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing grep parameters: %w", err)
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	baseDir := t.workDir
	if params.Path != "" {
		if filepath.IsAbs(params.Path) {
			baseDir = params.Path
		} else {
			baseDir = filepath.Join(t.workDir, params.Path)
		}
	}

	// Find files to search
	filePattern := "**/*"
	if params.Include != "" {
		filePattern = "**/" + params.Include
	}

	fullPattern := filepath.Join(baseDir, filePattern)
	files, err := doublestar.FilepathGlob(fullPattern)
	if err != nil {
		return "", fmt.Errorf("finding files: %w", err)
	}

	var results []string
	maxResults := 100

	for _, filePath := range files {
		if ctx.Err() != nil {
			break
		}

		// Skip directories and binary files
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() {
			continue
		}
		// Skip large files (> 1MB)
		if info.Size() > 1<<20 {
			continue
		}

		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(t.workDir, filePath)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", relPath, lineNum, line))
				if len(results) >= maxResults {
					f.Close()
					results = append(results, fmt.Sprintf("\n(truncated at %d results)", maxResults))
					return strings.Join(results, "\n"), nil
				}
			}
		}
		f.Close()
	}

	if len(results) == 0 {
		return "No matches found.", nil
	}

	return strings.Join(results, "\n"), nil
}
