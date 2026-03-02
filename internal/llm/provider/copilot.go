package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/nullism/goder/internal/message"
)

// CopilotProvider implements the Provider interface for GitHub Copilot.
// It supports both the Chat Completions API and the Responses API,
// routing automatically based on the selected model.
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

// fallbackCopilotModels is used when fetching from models.dev fails.
var fallbackCopilotModels = []string{
	"claude-opus-4.6",
	"claude-sonnet-4",
	"claude-sonnet-4.5",
	"gemini-2.5-pro",
	"gpt-4.1",
	"gpt-4o",
	"gpt-5",
	"gpt-5.1-codex",
	"gpt-5.2-codex",
	"o3-mini",
	"o4-mini",
}

// modelsDevURL is the URL for the models.dev registry.
const modelsDevURL = "https://models.dev/api.json"

// modelsDevResponse represents the relevant portion of the models.dev JSON.
// The top-level object is keyed by provider ID; each provider has a "models"
// map keyed by model ID.
type modelsDevResponse map[string]modelsDevProvider

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID string `json:"id"`
}

// ListModels fetches the Copilot model list from the models.dev registry.
// If the fetch fails, it falls back to a hardcoded list.
func (p *CopilotProvider) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", modelsDevURL, nil)
	if err != nil {
		return fallbackCopilotModels, nil
	}

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fallbackCopilotModels, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fallbackCopilotModels, nil
	}

	var registry modelsDevResponse
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return fallbackCopilotModels, nil
	}

	provider, ok := registry["github-copilot"]
	if !ok || len(provider.Models) == 0 {
		return fallbackCopilotModels, nil
	}

	var models []string
	for id := range provider.Models {
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}

// useResponsesAPI returns true if the given model should use the
// Responses API (/responses) instead of Chat Completions (/chat/completions).
// GPT-5 and later (except gpt-5-mini) require the Responses API.
func useResponsesAPI(model string) bool {
	if strings.HasPrefix(model, "gpt-5-mini") {
		return false
	}
	// Match gpt-5, gpt-5.1, gpt-5.1-codex, gpt-6, etc.
	if len(model) < 5 || model[:4] != "gpt-" {
		return false
	}
	rest := model[4:]
	// First character after "gpt-" must be a digit >= 5.
	if rest[0] < '5' || rest[0] > '9' {
		return false
	}
	return true
}

// --- Responses API types ---

// responsesInputItem is a union type for the Responses API input array.
// Role-based items use Role/Content; function calls and results use Type.
type responsesInputItem struct {
	// For role-based items (user, assistant, system/developer).
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`

	// For function_call items.
	Type      string `json:"type,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// For function_call_output items.
	Output string `json:"output,omitempty"`
}

// responsesTextContent is a content part for the Responses API.
type responsesTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// responsesTool is a tool definition for the Responses API.
type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// responsesRequest is the request body for POST /responses.
type responsesRequest struct {
	Model           string               `json:"model"`
	Instructions    string               `json:"instructions,omitempty"`
	Input           []responsesInputItem `json:"input"`
	Tools           []responsesTool      `json:"tools,omitempty"`
	Stream          bool                 `json:"stream"`
	Store           bool                 `json:"store"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
}

// responsesEvent is a generic SSE event from the Responses API stream.
type responsesEvent struct {
	Type string `json:"type"`

	// For response.created / response.completed / response.incomplete.
	Response *responsesResponseObj `json:"response,omitempty"`

	// For response.output_item.added / response.output_item.done.
	OutputIndex int               `json:"output_index,omitempty"`
	Item        *responsesItemObj `json:"item,omitempty"`

	// For response.output_text.delta / response.function_call_arguments.delta.
	ItemID string `json:"item_id,omitempty"`
	Delta  string `json:"delta,omitempty"`

	// For error events.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// responsesResponseObj is the response object in lifecycle events.
type responsesResponseObj struct {
	ID                string             `json:"id,omitempty"`
	IncompleteDetails *incompleteDetails `json:"incomplete_details,omitempty"`
	Usage             *responsesUsage    `json:"usage,omitempty"`
}

type incompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

// responsesUsage holds token usage from the Responses API.
type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// responsesItemObj is an output item in the Responses API stream.
type responsesItemObj struct {
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Status    string `json:"status,omitempty"`
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

// SendMessage sends a streaming request to the Copilot API, using either
// the Responses API or Chat Completions API based on the model.
func (p *CopilotProvider) SendMessage(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if useResponsesAPI(p.model) {
		return p.sendResponses(ctx, req)
	}
	return p.sendChatCompletions(ctx, req)
}

// sendChatCompletions sends a streaming request via the Chat Completions API.
func (p *CopilotProvider) sendChatCompletions(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	messages := p.buildMessages(req)

	var tools []chatTool
	for _, t := range req.Tools {
		tools = append(tools, chatTool{
			Type:     "function",
			Function: chatFunction(t),
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
		return nil, fmt.Errorf("copilot API error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
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

// --- Responses API implementation ---

// sendResponses sends a streaming request via the Responses API.
func (p *CopilotProvider) sendResponses(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	input := p.buildResponsesInput(req)

	var tools []responsesTool
	for _, t := range req.Tools {
		tools = append(tools, responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	respReq := responsesRequest{
		Model:           p.model,
		Instructions:    req.SystemPrompt,
		Input:           input,
		Tools:           tools,
		Stream:          true,
		Store:           false,
		MaxOutputTokens: maxTokens,
	}

	body, err := json.Marshal(respReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/responses", bytes.NewReader(body))
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
		return nil, fmt.Errorf("copilot API error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	events := make(chan StreamEvent, 64)

	go func() {
		defer close(events)
		defer resp.Body.Close()

		p.processResponsesStream(ctx, resp.Body, events)
	}()

	return events, nil
}

// buildResponsesInput converts our message format to the Responses API input array.
func (p *CopilotProvider) buildResponsesInput(req Request) []responsesInputItem {
	var input []responsesInputItem

	for _, msg := range req.Messages {
		switch msg.Role {
		case message.User:
			input = append(input, responsesInputItem{
				Role: "user",
				Content: []responsesTextContent{
					{Type: "input_text", Text: msg.Content},
				},
			})

		case message.Assistant:
			// Add the text content as an assistant message if present.
			if msg.Content != "" {
				input = append(input, responsesInputItem{
					Role: "assistant",
					Content: []responsesTextContent{
						{Type: "output_text", Text: msg.Content},
					},
				})
			}
			// Each tool call becomes a separate function_call item.
			for _, tc := range msg.ToolCalls {
				input = append(input, responsesInputItem{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Name,
					Arguments: string(tc.Input),
				})
			}

		case message.Tool:
			// Each tool result becomes a function_call_output item.
			for _, tr := range msg.ToolResults {
				input = append(input, responsesInputItem{
					Type:   "function_call_output",
					CallID: tr.ToolCallID,
					Output: tr.Output,
				})
			}

		case message.System:
			input = append(input, responsesInputItem{
				Role:    "developer",
				Content: msg.Content,
			})
		}
	}

	return input
}

// processResponsesStream reads the SSE stream from the Responses API and emits events.
func (p *CopilotProvider) processResponsesStream(ctx context.Context, body io.Reader, events chan<- StreamEvent) {
	// Track ongoing tool calls by output_index.
	type toolCallState struct {
		callID    string
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

		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			events <- StreamEvent{Type: EventDone}
			return
		}

		var evt responsesEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "response.output_item.added":
			if evt.Item == nil {
				continue
			}
			if evt.Item.Type == "function_call" {
				state := &toolCallState{
					callID: evt.Item.CallID,
					name:   evt.Item.Name,
				}
				toolCalls[evt.OutputIndex] = state
				if state.callID != "" && state.name != "" {
					state.started = true
					events <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   state.callID,
						ToolCallName: state.name,
					}
				}
			}

		case "response.output_text.delta":
			if evt.Delta != "" {
				events <- StreamEvent{
					Type: EventTextDelta,
					Text: evt.Delta,
				}
			}

		case "response.function_call_arguments.delta":
			state, ok := toolCalls[evt.OutputIndex]
			if !ok {
				continue
			}
			if evt.Delta != "" {
				state.arguments.WriteString(evt.Delta)
				events <- StreamEvent{
					Type:          EventToolCallDelta,
					ToolCallID:    state.callID,
					ToolCallName:  state.name,
					ToolCallInput: evt.Delta,
				}
			}

		case "response.output_item.done":
			if evt.Item == nil || evt.Item.Type != "function_call" {
				continue
			}
			state, ok := toolCalls[evt.OutputIndex]
			if !ok {
				continue
			}
			if state.started {
				events <- StreamEvent{
					Type:          EventToolCallEnd,
					ToolCallID:    state.callID,
					ToolCallName:  state.name,
					ToolCallInput: state.arguments.String(),
				}
			}
			delete(toolCalls, evt.OutputIndex)

		case "response.completed", "response.incomplete":
			// Finalize any remaining tool calls.
			for idx, state := range toolCalls {
				if state.started {
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    state.callID,
						ToolCallName:  state.name,
						ToolCallInput: state.arguments.String(),
					}
				}
				delete(toolCalls, idx)
			}
			var usage Usage
			if evt.Response != nil && evt.Response.Usage != nil {
				usage = Usage{
					InputTokens:  evt.Response.Usage.InputTokens,
					OutputTokens: evt.Response.Usage.OutputTokens,
					TotalTokens:  evt.Response.Usage.InputTokens + evt.Response.Usage.OutputTokens,
				}
			}
			events <- StreamEvent{Type: EventDone, Usage: usage}
			return

		case "error":
			events <- StreamEvent{
				Type:  EventError,
				Error: fmt.Errorf("copilot responses API error: [%s] %s", evt.Code, evt.Message),
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		events <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	events <- StreamEvent{Type: EventDone}
}
