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

	"github.com/webgovernor/goder/internal/message"
)

// OpenAIProvider implements the Provider interface for OpenAI's API
// using the Responses API (POST /v1/responses).
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

// SetAPIKey updates the provider's API key at runtime.
func (p *OpenAIProvider) SetAPIKey(apiKey string) { p.apiKey = apiKey }

// SetModel updates the provider's model at runtime.
func (p *OpenAIProvider) SetModel(model string) { p.model = model }

// oaiModelsResponse is the response from GET /v1/models.
type oaiModelsResponse struct {
	Data []oaiModelEntry `json:"data"`
}

type oaiModelEntry struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by"`
}

// supportedModelPrefixes are prefixes that identify models usable for text generation.
var supportedModelPrefixes = []string{"gpt-", "o1", "o3", "o4", "chatgpt-"}

// ListModels fetches available models from the OpenAI API and returns
// only text-generation-capable model IDs, sorted alphabetically.
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI API error (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	var modelsResp oaiModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("decoding models response: %w", err)
	}

	var models []string
	for _, m := range modelsResp.Data {
		if isSupportedModel(m.ID) {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

// isSupportedModel returns true if the model ID looks like a text-generation model
// supported by the Responses API.
func isSupportedModel(id string) bool {
	for _, prefix := range supportedModelPrefixes {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// --- Responses API types ---

// respInputItem represents an input item for the Responses API.
// Different item types use different subsets of fields; we use
// json.RawMessage for content to support both string and structured forms,
// but for simplicity we keep separate types and marshal them explicitly.
type respInputItem map[string]interface{}

// respTool is the flat tool definition format used by the Responses API.
type respTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// respRequest is the request body for POST /v1/responses.
type respRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           []respInputItem `json:"input"`
	Tools           []respTool      `json:"tools,omitempty"`
	Stream          bool            `json:"stream"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Store           bool            `json:"store"`
}

// respStreamEvent is the generic SSE event from the Responses API.
type respStreamEvent struct {
	Type string          `json:"type"`
	Item json.RawMessage `json:"item,omitempty"`

	// For delta events
	ContentIndex int    `json:"content_index,omitempty"`
	OutputIndex  int    `json:"output_index,omitempty"`
	Delta        string `json:"delta,omitempty"`
	ItemID       string `json:"item_id,omitempty"`

	// For response-level events
	Response json.RawMessage `json:"response,omitempty"`
}

// respOutputItem is an item in the response output array.
type respOutputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "message", "function_call"
	Role      string `json:"role,omitempty"`
	Name      string `json:"name,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// respResponseBody is the full response object (used in response.completed).
type respResponseBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

// SendMessage sends a streaming request to OpenAI's Responses API and returns events on a channel.
func (p *OpenAIProvider) SendMessage(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	// Build the input array
	input := p.buildInput(req)

	// Build the tools array in Responses API format (flat)
	var tools []respTool
	for _, t := range req.Tools {
		tools = append(tools, respTool{
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

	respReq := respRequest{
		Model:           p.model,
		Instructions:    req.SystemPrompt,
		Input:           input,
		Tools:           tools,
		Stream:          true,
		MaxOutputTokens: maxTokens,
		Store:           false,
	}

	body, err := json.Marshal(respReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/responses", bytes.NewReader(body))
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

// buildInput converts our message format to the Responses API input format.
func (p *OpenAIProvider) buildInput(req Request) []respInputItem {
	var items []respInputItem

	// Note: SystemPrompt is handled via the top-level "instructions" field,
	// so we don't add it as an input item.

	for _, msg := range req.Messages {
		switch msg.Role {
		case message.User:
			items = append(items, respInputItem{
				"role":    "user",
				"content": msg.Content,
			})

		case message.Assistant:
			// Add the assistant message
			if msg.Content != "" {
				items = append(items, respInputItem{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
			// Add function calls as separate items
			for _, tc := range msg.ToolCalls {
				items = append(items, respInputItem{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Name,
					"arguments": string(tc.Input),
				})
			}

		case message.Tool:
			// Tool results - one item per result
			for _, tr := range msg.ToolResults {
				items = append(items, respInputItem{
					"type":    "function_call_output",
					"call_id": tr.ToolCallID,
					"output":  tr.Output,
				})
			}

		case message.System:
			// Additional system messages go as developer role items
			items = append(items, respInputItem{
				"role":    "developer",
				"content": msg.Content,
			})
		}
	}

	return items
}

// processStream reads the SSE stream from the Responses API and emits events.
func (p *OpenAIProvider) processStream(ctx context.Context, body io.Reader, events chan<- StreamEvent) {
	// Track function calls being built up across events
	type funcCallState struct {
		id        string
		name      string
		arguments strings.Builder
		started   bool
	}
	funcCalls := make(map[string]*funcCallState) // keyed by item_id

	scanner := bufio.NewScanner(body)
	// Increase buffer for large responses
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			events <- StreamEvent{Type: EventError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// Skip empty lines and SSE comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// SSE event type line (we parse it but primarily use the JSON type field)
		if strings.HasPrefix(line, "event: ") {
			continue
		}

		// Parse SSE data lines
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var evt respStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue // skip malformed events
		}

		switch evt.Type {

		// --- Text output events ---
		case "response.output_text.delta":
			if evt.Delta != "" {
				events <- StreamEvent{
					Type: EventTextDelta,
					Text: evt.Delta,
				}
			}

		// --- Function call events ---
		case "response.output_item.added":
			// A new output item was added; check if it's a function call
			var item respOutputItem
			if err := json.Unmarshal(evt.Item, &item); err != nil {
				continue
			}
			if item.Type == "function_call" {
				state := &funcCallState{
					id:   item.CallID,
					name: item.Name,
				}
				funcCalls[item.ID] = state

				// Emit start event if we have enough info
				if state.id != "" && state.name != "" {
					state.started = true
					events <- StreamEvent{
						Type:         EventToolCallStart,
						ToolCallID:   state.id,
						ToolCallName: state.name,
					}
				}
			}

		case "response.function_call_arguments.delta":
			if evt.Delta != "" {
				state, ok := funcCalls[evt.ItemID]
				if !ok {
					// Create a placeholder state if we missed the added event
					state = &funcCallState{}
					funcCalls[evt.ItemID] = state
				}

				state.arguments.WriteString(evt.Delta)
				events <- StreamEvent{
					Type:          EventToolCallDelta,
					ToolCallID:    state.id,
					ToolCallName:  state.name,
					ToolCallInput: evt.Delta,
				}
			}

		case "response.function_call_arguments.done":
			state, ok := funcCalls[evt.ItemID]
			if ok {
				// Use the complete arguments from the done event if provided
				finalArgs := state.arguments.String()
				if evt.Delta != "" {
					// Some implementations send the full args in the done event
					finalArgs = evt.Delta
				}
				events <- StreamEvent{
					Type:          EventToolCallEnd,
					ToolCallID:    state.id,
					ToolCallName:  state.name,
					ToolCallInput: finalArgs,
				}
				delete(funcCalls, evt.ItemID)
			}

		case "response.output_item.done":
			// An output item is complete. If it's a function call that wasn't
			// finalized via arguments.done, handle it here.
			var item respOutputItem
			if err := json.Unmarshal(evt.Item, &item); err != nil {
				continue
			}
			if item.Type == "function_call" {
				// Check if we already emitted ToolCallEnd
				if state, ok := funcCalls[item.ID]; ok {
					finalArgs := state.arguments.String()
					if item.Arguments != "" {
						finalArgs = item.Arguments
					}
					if !state.started {
						// Emit start if we haven't yet
						events <- StreamEvent{
							Type:         EventToolCallStart,
							ToolCallID:   item.CallID,
							ToolCallName: item.Name,
						}
					}
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    item.CallID,
						ToolCallName:  item.Name,
						ToolCallInput: finalArgs,
					}
					delete(funcCalls, item.ID)
				}
			}

		// --- Response lifecycle events ---
		case "response.completed":
			// Emit end events for any remaining function calls
			for id, state := range funcCalls {
				if state.started {
					events <- StreamEvent{
						Type:          EventToolCallEnd,
						ToolCallID:    state.id,
						ToolCallName:  state.name,
						ToolCallInput: state.arguments.String(),
					}
				}
				delete(funcCalls, id)
			}
			events <- StreamEvent{Type: EventDone}
			return

		case "response.failed":
			var respBody respResponseBody
			if err := json.Unmarshal(evt.Response, &respBody); err == nil && respBody.Error != nil {
				events <- StreamEvent{
					Type:  EventError,
					Error: fmt.Errorf("OpenAI API error (%s): %s", respBody.Error.Code, respBody.Error.Message),
				}
			} else {
				events <- StreamEvent{
					Type:  EventError,
					Error: fmt.Errorf("response failed"),
				}
			}
			return

		case "response.incomplete":
			events <- StreamEvent{
				Type:  EventError,
				Error: fmt.Errorf("response incomplete (model stopped early)"),
			}
			return

		// Events we acknowledge but don't need to act on:
		// response.created, response.in_progress,
		// response.output_item.added (non-function_call),
		// response.content_part.added, response.content_part.done,
		// response.output_text.done, response.output_text.annotation.added
		default:
			// Ignore unknown/unhandled event types
		}
	}

	if err := scanner.Err(); err != nil {
		events <- StreamEvent{Type: EventError, Error: fmt.Errorf("reading stream: %w", err)}
		return
	}

	// If we got here without response.completed, emit done anyway
	events <- StreamEvent{Type: EventDone}
}
