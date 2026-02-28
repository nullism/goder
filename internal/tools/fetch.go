package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchTool fetches content from URLs.
type FetchTool struct{}

// NewFetchTool creates a new fetch tool.
func NewFetchTool() *FetchTool {
	return &FetchTool{}
}

func (t *FetchTool) Name() string { return "fetch" }

func (t *FetchTool) Description() string {
	return "Fetch content from a URL. Returns the response body as text. Useful for reading documentation, APIs, or web pages."
}

func (t *FetchTool) Parameters() json.RawMessage {
	schema := ToolDef{
		Type: "object",
		Properties: map[string]Property{
			"url": {
				Type:        "string",
				Description: "The URL to fetch content from.",
			},
			"timeout": {
				Type:        "number",
				Description: "Optional timeout in seconds. Defaults to 30.",
			},
		},
		Required: []string{"url"},
	}
	data, _ := json.Marshal(schema)
	return data
}

func (t *FetchTool) RequiresPermission() bool { return false }

func (t *FetchTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var params struct {
		URL     string `json:"url"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("parsing fetch parameters: %w", err)
	}

	if params.Timeout <= 0 {
		params.Timeout = 30
	}

	// Ensure URL starts with http(s)
	url := params.URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	client := &http.Client{
		Timeout: time.Duration(params.Timeout) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "goder/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Limit reading to 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	result := string(body)
	if len(result) == 0 {
		return "(empty response)", nil
	}

	return result, nil
}
