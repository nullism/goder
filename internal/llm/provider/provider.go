package provider

import (
	"context"
	"encoding/json"

	"github.com/webgovernor/goder/internal/message"
	"github.com/webgovernor/goder/internal/tools"
)

// StreamEventType identifies the type of streaming event.
type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventToolCallStart
	EventToolCallDelta
	EventToolCallEnd
	EventDone
	EventError
)

// StreamEvent represents a single event in a streaming LLM response.
type StreamEvent struct {
	Type StreamEventType

	// For TextDelta events
	Text string

	// For ToolCall events
	ToolCallID    string
	ToolCallName  string
	ToolCallInput string // accumulated JSON input (for End events, this is the complete input)

	// For Error events
	Error error
}

// ToolDefinition is the provider-agnostic representation of a tool for the LLM.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Request represents a request to the LLM provider.
type Request struct {
	SystemPrompt string
	Messages     []message.Message
	Tools        []ToolDefinition
	MaxTokens    int
}

// Provider defines the interface for LLM providers.
type Provider interface {
	// Name returns the provider's identifier.
	Name() string

	// SendMessage sends a request to the LLM and returns a channel of streaming events.
	SendMessage(ctx context.Context, req Request) (<-chan StreamEvent, error)

	// ListModels returns the available model IDs from the provider.
	ListModels(ctx context.Context) ([]string, error)

	// SetAPIKey updates the provider's API key at runtime.
	SetAPIKey(apiKey string)

	// SetModel updates the provider's model at runtime.
	SetModel(model string)
}

// ToolsToDefinitions converts a tools.Registry into provider ToolDefinitions.
func ToolsToDefinitions(registry *tools.Registry) []ToolDefinition {
	allTools := registry.All()
	defs := make([]ToolDefinition, 0, len(allTools))
	for _, t := range allTools {
		defs = append(defs, ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}
