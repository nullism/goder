package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/webgovernor/goder/internal/message"
)

// CopilotProvider implements the Provider interface for GitHub Copilot
// using the OpenAI-compatible Chat Completions API.
type CopilotProvider struct {
	apiKey  string
	model   string
	baseURL string
}

// NewCopilotProvider creates a new GitHub Copilot provider.
func NewCopilotProvider(apiKey, model string) *CopilotProvider {
	if model == "" {
		model = "gpt-4o" // default Copilot model
	}
	return &CopilotProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.githubcopilot.com",
	}
}

func (p *CopilotProvider) Name() string { return "copilot" }

// SetAPIKey updates the provider's API key at runtime.
func (p *CopilotProvider) SetAPIKey(apiKey string) { p.apiKey = apiKey }

// SetModel updates the provider's model at runtime.
func (p *CopilotProvider) SetModel(model string) { p.model = model }

// copilotModels is the list of known models available through GitHub Copilot.
var copilotModels = []string{
	"claude-sonnet-4-20250514",
	"gemini-2.0-flash",
	"gpt-4o",
	"gpt-4.1",
	"o3-mini",
	"o4-mini",
}

// ListModels returns the known models available through GitHub Copilot.
func (p *CopilotProvider) ListModels(ctx context.Context) ([]string, error) {
	return copilotModels, nil
}

// --- Chat Completions API types ---

// chatMessage represents a message in the Chat Completions API format.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

// chatToolCall represents a tool call in the Chat Completions API format.
type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

// chatFunctionCall holds the function name and arguments.
type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatTool is the tool definition format for the Chat Completions API.
type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

// chatFunction describes a function tool.
type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatRequest is the request body for POST /chat/completions.
type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	Tools      []chatTool    `json:"tools,omitempty"`
	Stream     bool          `json:"stream"`
	MaxTokens  int           `json:"max_tokens,omitempty"`
	StreamOpts *streamOpts   `json:"stream_options,omitempty"`
}

// streamOpts controls streaming behavior.
type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

// chatChunk represents a single SSE chunk from the streaming Chat Completions API.
type chatChunk struct {
	ID      string       `json:"id"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

// chatChoice is a single choice in a streaming chunk.
type chatChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

// chatDelta holds the incremental content in a streaming chunk.
type chatDelta struct {
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolCalls []chatToolDelta `json:"tool_calls,omitempty"`
}

// chatToolDelta represents a tool call delta in streaming.
type chatToolDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function chatFunctionDelta `json:"function,omitempty"`
}

// chatFunctionDelta holds incremental function call data.
type chatFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// chatUsage holds token usage information.
type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// SendMessage sends a streaming request to the Copilot Chat Completions API.
func (p *CopilotProvider) SendMessage(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	messages := p.buildMessages(req)

	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	chatReq := chatRequest{
		Model:      p.model,
		Messages:   messages,
		Tools:      tools,
		Stream:     true,
		MaxTokens:  maxTokens,
		StreamOpts: &streamOpts{IncludeUsage: true},
	}

	body, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Openai-Intent", "conversation-edits")
	httpReq.Header.Set("User-Agent", "goder/0.1")

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Copilot API error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	events := make(chan StreamEvent, 64)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		p.processStream(ctx, resp.Body, events)
	}()

	return events, nil
}

// buildMessages converts our message format to the Chat Completions API format.
func (p *CopilotProvider) buildMessages(req Request) []chatMessage {
	var messages []chatMessage

	// System prompt as a system message.
	if req.SystemPrompt != "" {
		messages = append(messages, chatMessage{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case message.User:
			messages = append(messages, chatMessage{
				Role:    "user",
				Content: msg.Content,
			})

		case message.Assistant:
			cm := chatMessage{
				Role:    "assistant",
				Content: msg.Content,
			}
			// Convert tool calls to Chat Completions format.
			for _, tc := range msg.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}
			messages = append(messages, cm)

		case message.Tool:
			// Each tool result becomes a separate message with role "tool".
			for _, tr := range msg.ToolResults {
				messages = append(messages, chatMessage{
					Role:       "tool",
					Content:    tr.Output,
					ToolCallID: tr.ToolCallID,
					Name:       tr.Name,
				})
			}

		case message.System:
			messages = append(messages, chatMessage{
				Role:    "system",
				Content: msg.Content,
			})
		}
	}

	return messages
}

// processStream reads the SSE stream from the Chat Completions API and emits events.
func (p *CopilotProvider) processStream(ctx context.Context, body io.Reader, events chan<- StreamEvent) {
	// Track tool calls being built up across streaming chunks.
	// Keyed by the tool call index within the response.
	type toolCallState struct {
		id        string
		name      string
		arguments strings.Builder
		started   bool
	}
	toolCalls := make(map[int]*toolCallState)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			events <- StreamEvent{Type: EventError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// Skip empty lines and SSE comments.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// End of stream marker.
		if data == "[DONE]" {
			// Finalize any remaining tool calls.
			for idx, state := range toolCalls {
				if state.started {
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    state.id,
						ToolCallName:  state.name,
						ToolCallInput: state.arguments.String(),
					}
				}
				delete(toolCalls, idx)
			}
			events <- StreamEvent{Type: EventDone}
			return
		}

		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunks
		}

		// Handle usage info (sent with stream_options.include_usage).
		if chunk.Usage != nil {
			// Usage comes in a final chunk; we'll include it with EventDone below.
			// Finalize any remaining tool calls.
			for idx, state := range toolCalls {
				if state.started {
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    state.id,
						ToolCallName:  state.name,
						ToolCallInput: state.arguments.String(),
					}
				}
				delete(toolCalls, idx)
			}
			events <- StreamEvent{
				Type: EventDone,
				Usage: Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					TotalTokens:  chunk.Usage.TotalTokens,
				},
			}
			return
		}

		for _, choice := range chunk.Choices {
			delta := choice.Delta

			// Text content.
			if delta.Content != "" {
				events <- StreamEvent{
					Type: EventTextDelta,
					Text: delta.Content,
				}
			}

			// Tool call deltas.
			for _, tcDelta := range delta.ToolCalls {
				state, ok := toolCalls[tcDelta.Index]
				if !ok {
					state = &toolCallState{}
					toolCalls[tcDelta.Index] = state
				}

				// First chunk for this tool call includes id and name.
				if tcDelta.ID != "" {
					state.id = tcDelta.ID
				}
				if tcDelta.Function.Name != "" {
					state.name = tcDelta.Function.Name
				}

				// Emit start event once we have id and name.
				if !state.started && state.id != "" && state.name != "" {
					state.started = true
					events <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   state.id,
						ToolCallName: state.name,
					}
				}

				// Accumulate arguments.
				if tcDelta.Function.Arguments != "" {
					state.arguments.WriteString(tcDelta.Function.Arguments)
					events <- StreamEvent{
						Type:          EventToolCallDelta,
						ToolCallID:    state.id,
						ToolCallName:  state.name,
						ToolCallInput: tcDelta.Function.Arguments,
					}
				}
			}

			// Check finish_reason for tool call completion.
			if choice.FinishReason != nil {
				switch *choice.FinishReason {
				case "tool_calls", "stop":
					// Finalize all pending tool calls.
					for idx, state := range toolCalls {
						if state.started {
							events <- StreamEvent{
								Type:          EventToolCallEnd,
								ToolCallID:    state.id,
								ToolCallName:  state.name,
								ToolCallInput: state.arguments.String(),
							}
						}
						delete(toolCalls, idx)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		events <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// If we got here without [DONE] or usage, emit done anyway.
	events <- StreamEvent{Type: EventDone}
}
