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

// OpenAIProvider implements the Provider interface for OpenAI's API.
type OpenAIProvider struct {
	apiKey  string
	model   string
	baseURL string
}

// NewOpenAIProvider creates a new OpenAI provider.
func NewOpenAIProvider(apiKey, model string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.openai.com/v1",
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

// openAI API types

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaiTool struct {
	Type     string          `json:"type"`
	Function oaiToolFunction `json:"function"`
}

type oaiToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Tools       []oaiTool    `json:"tools,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Stream      bool         `json:"stream"`
	Temperature *float64     `json:"temperature,omitempty"`
}

// SSE streaming types

type oaiStreamChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Choices []oaiStreamChoice `json:"choices"`
}

type oaiStreamChoice struct {
	Index        int            `json:"index"`
	Delta        oaiStreamDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type oaiStreamDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls []oaiStreamToolCall `json:"tool_calls,omitempty"`
}

type oaiStreamToolCall struct {
	Index    int         `json:"index"`
	ID       string      `json:"id,omitempty"`
	Type     string      `json:"type,omitempty"`
	Function oaiFunction `json:"function"`
}

// SendMessage sends a streaming request to OpenAI and returns events on a channel.
func (p *OpenAIProvider) SendMessage(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	// Build the messages array
	messages := p.buildMessages(req)

	// Build the tools array
	var oaiTools []oaiTool
	for _, t := range req.Tools {
		oaiTools = append(oaiTools, oaiTool{
			Type: "function",
			Function: oaiToolFunction{
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

	oaiReq := oaiRequest{
		Model:     p.model,
		Messages:  messages,
		Tools:     oaiTools,
		MaxTokens: maxTokens,
		Stream:    true,
	}

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	events := make(chan StreamEvent, 64)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		p.processStream(ctx, resp.Body, events)
	}()

	return events, nil
}

// buildMessages converts our message format to OpenAI's format.
func (p *OpenAIProvider) buildMessages(req Request) []oaiMessage {
	var messages []oaiMessage

	// System prompt
	if req.SystemPrompt != "" {
		messages = append(messages, oaiMessage{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	// Conversation messages
	for _, msg := range req.Messages {
		switch msg.Role {
		case message.User:
			messages = append(messages, oaiMessage{
				Role:    "user",
				Content: msg.Content,
			})
		case message.Assistant:
			m := oaiMessage{
				Role:    "assistant",
				Content: msg.Content,
			}
			// Include tool calls if present
			for _, tc := range msg.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, oaiToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: oaiFunction{
						Name:      tc.Name,
						Arguments: string(tc.Input),
					},
				})
			}
			messages = append(messages, m)
		case message.Tool:
			// Tool results - one message per result
			for _, tr := range msg.ToolResults {
				messages = append(messages, oaiMessage{
					Role:       "tool",
					Content:    tr.Output,
					ToolCallID: tr.ToolCallID,
				})
			}
		case message.System:
			messages = append(messages, oaiMessage{
				Role:    "system",
				Content: msg.Content,
			})
		}
	}

	return messages
}

// processStream reads the SSE stream and emits events.
func (p *OpenAIProvider) processStream(ctx context.Context, body io.Reader, events chan<- StreamEvent) {
	// Track tool calls being built up across chunks
	type toolCallState struct {
		id        string
		name      string
		arguments strings.Builder
		started   bool
	}
	toolCalls := make(map[int]*toolCallState)

	scanner := bufio.NewScanner(body)
	// Increase buffer for large responses
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			events <- StreamEvent{Type: EventError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE data lines
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			// Emit end events for any remaining tool calls
			for idx, tc := range toolCalls {
				if tc.started {
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    tc.id,
						ToolCallName:  tc.name,
						ToolCallInput: tc.arguments.String(),
					}
				}
				delete(toolCalls, idx)
			}
			events <- StreamEvent{Type: EventDone}
			return
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunks
		}

		for _, choice := range chunk.Choices {
			delta := choice.Delta

			// Handle text content
			if delta.Content != nil && *delta.Content != "" {
				events <- StreamEvent{
					Type: EventTextDelta,
					Text: *delta.Content,
				}
			}

			// Handle tool calls
			for _, tc := range delta.ToolCalls {
				idx := tc.Index

				state, exists := toolCalls[idx]
				if !exists {
					state = &toolCallState{}
					toolCalls[idx] = state
				}

				// First chunk for this tool call has the ID and name
				if tc.ID != "" {
					state.id = tc.ID
				}
				if tc.Function.Name != "" {
					state.name = tc.Function.Name
				}

				// Emit start event once we have ID and name
				if !state.started && state.id != "" && state.name != "" {
					state.started = true
					events <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   state.id,
						ToolCallName: state.name,
					}
				}

				// Accumulate arguments
				if tc.Function.Arguments != "" {
					state.arguments.WriteString(tc.Function.Arguments)
					events <- StreamEvent{
						Type:          EventToolCallDelta,
						ToolCallID:    state.id,
						ToolCallName:  state.name,
						ToolCallInput: tc.Function.Arguments,
					}
				}
			}

			// Handle finish reason
			if choice.FinishReason != nil {
				// Emit end events for tool calls
				for idx, tc := range toolCalls {
					if tc.started {
						events <- StreamEvent{
							Type:          EventToolCallEnd,
							ToolCallID:    tc.id,
							ToolCallName:  tc.name,
							ToolCallInput: tc.arguments.String(),
						}
					}
					delete(toolCalls, idx)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		events <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// If we got here without [DONE], emit done anyway
	events <- StreamEvent{Type: EventDone}
}
