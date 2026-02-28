package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/webgovernor/goder/internal/llm/prompt"
	"github.com/webgovernor/goder/internal/llm/provider"
	"github.com/webgovernor/goder/internal/message"
	"github.com/webgovernor/goder/internal/permission"
	"github.com/webgovernor/goder/internal/tools"
)

// DefaultMaxIterations is the default limit for the agent loop to prevent infinite loops.
const DefaultMaxIterations = 25

// Event types sent from the agent to the TUI.
type EventType int

const (
	EventStreamText EventType = iota
	EventToolCallStart
	EventToolCallEnd
	EventToolResult
	EventAgentDone
	EventAgentError
	EventPermissionRequest
	EventPersistMessage // intermediate message that should be saved to DB
)

// Event is sent from the agent loop to the TUI for rendering.
type Event struct {
	Type EventType

	// For StreamText
	Text string

	// For ToolCall events
	ToolCallID   string
	ToolCallName string
	ToolInput    string
	ToolOutput   string
	ToolIsError  bool

	// For errors
	Error error

	// For Done - the final complete message
	FinalMessage *message.Message

	// For PermissionRequest
	PermissionReq *permission.Request
}

// Agent orchestrates the LLM + tool execution loop.
type Agent struct {
	provider      provider.Provider
	registry      *tools.Registry
	permSvc       *permission.Service
	workDir       string
	mode          string
	maxTokens     int
	maxIterations int
}

// Config holds agent construction parameters.
type Config struct {
	Provider      provider.Provider
	Registry      *tools.Registry
	PermSvc       *permission.Service
	WorkDir       string
	Mode          string
	MaxTokens     int
	MaxIterations int
}

// New creates a new Agent.
func New(cfg Config) *Agent {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}
	return &Agent{
		provider:      cfg.Provider,
		registry:      cfg.Registry,
		permSvc:       cfg.PermSvc,
		workDir:       cfg.WorkDir,
		mode:          cfg.Mode,
		maxTokens:     cfg.MaxTokens,
		maxIterations: maxIter,
	}
}

// SetMode updates the agent's operating mode.
func (a *Agent) SetMode(mode string) {
	a.mode = mode
}

// Run executes the agent loop. It sends events on the returned channel.
// The caller should read from the channel until it is closed.
// history should contain all previous messages in the conversation.
func (a *Agent) Run(ctx context.Context, history []message.Message, sessionID string) <-chan Event {
	events := make(chan Event, 64)

	go func() {
		defer close(events)
		a.runLoop(ctx, history, sessionID, events)
	}()

	return events
}

func (a *Agent) runLoop(ctx context.Context, history []message.Message, sessionID string, events chan<- Event) {
	systemPrompt := prompt.BuildSystemPrompt(a.mode, a.workDir, a.registry)

	// Build tool definitions, filtering by mode
	toolDefs := a.buildToolDefs()

	currentHistory := make([]message.Message, len(history))
	copy(currentHistory, history)

	for iteration := 0; iteration < a.maxIterations; iteration++ {
		if ctx.Err() != nil {
			events <- Event{Type: EventAgentError, Error: ctx.Err()}
			return
		}

		// Send to LLM
		req := provider.Request{
			SystemPrompt: systemPrompt,
			Messages:     currentHistory,
			Tools:        toolDefs,
			MaxTokens:    a.maxTokens,
		}

		streamCh, err := a.provider.SendMessage(ctx, req)
		if err != nil {
			events <- Event{Type: EventAgentError, Error: fmt.Errorf("LLM request failed: %w", err)}
			return
		}

		// Accumulate the response
		var textContent strings.Builder
		var toolCalls []message.ToolCall
		type pendingToolCall struct {
			id   string
			name string
			args strings.Builder
		}
		pendingCalls := make(map[string]*pendingToolCall)

		var usage provider.Usage

		for event := range streamCh {
			switch event.Type {
			case provider.EventTextDelta:
				textContent.WriteString(event.Text)
				events <- Event{Type: EventStreamText, Text: event.Text}

			case provider.EventToolCallStart:
				pending := &pendingToolCall{
					id:   event.ToolCallID,
					name: event.ToolCallName,
				}
				pendingCalls[event.ToolCallID] = pending
				events <- Event{
					Type:         EventToolCallStart,
					ToolCallID:   event.ToolCallID,
					ToolCallName: event.ToolCallName,
				}

			case provider.EventToolCallDelta:
				if pending, ok := pendingCalls[event.ToolCallID]; ok {
					pending.args.WriteString(event.ToolCallInput)
				}

			case provider.EventToolCallEnd:
				if pending, ok := pendingCalls[event.ToolCallID]; ok {
					input := json.RawMessage(pending.args.String())
					// Use the final complete input from the event if available
					if event.ToolCallInput != "" {
						input = json.RawMessage(event.ToolCallInput)
					}
					toolCalls = append(toolCalls, message.ToolCall{
						ID:    pending.id,
						Name:  pending.name,
						Input: input,
					})
					events <- Event{
						Type:         EventToolCallEnd,
						ToolCallID:   pending.id,
						ToolCallName: pending.name,
						ToolInput:    string(input),
					}
					delete(pendingCalls, event.ToolCallID)
				}

			case provider.EventError:
				events <- Event{Type: EventAgentError, Error: event.Error}
				return

			case provider.EventDone:
				usage = event.Usage
				// handled below
			}
		}

		// Create the assistant message
		assistantMsg := message.NewAssistantMessage(sessionID, textContent.String(), toolCalls)
		assistantMsg.InputTokens = usage.InputTokens
		assistantMsg.OutputTokens = usage.OutputTokens
		assistantMsg.TotalTokens = usage.TotalTokens

		// Add to history
		currentHistory = append(currentHistory, assistantMsg)

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			events <- Event{Type: EventAgentDone, FinalMessage: &assistantMsg}
			return
		}

		// Persist the intermediate assistant message (with tool calls)
		events <- Event{Type: EventPersistMessage, FinalMessage: &assistantMsg}

		// Execute tool calls
		var toolResults []message.ToolResult
		for _, tc := range toolCalls {
			if ctx.Err() != nil {
				events <- Event{Type: EventAgentError, Error: ctx.Err()}
				return
			}

			result := a.executeTool(ctx, tc, events)
			toolResults = append(toolResults, result)

			events <- Event{
				Type:         EventToolResult,
				ToolCallID:   tc.ID,
				ToolCallName: tc.Name,
				ToolOutput:   result.Output,
				ToolIsError:  result.IsError,
			}
		}

		// Create tool result message and add to history
		toolResultMsg := message.NewToolResultMessage(sessionID, toolResults)
		currentHistory = append(currentHistory, toolResultMsg)

		// Persist the tool result message
		events <- Event{Type: EventPersistMessage, FinalMessage: &toolResultMsg}

		// Continue the loop - the LLM will see the tool results and respond
	}

	events <- Event{
		Type:  EventAgentError,
		Error: fmt.Errorf("agent reached maximum iterations (%d)", a.maxIterations),
	}
}

// executeTool runs a single tool call, handling permissions.
func (a *Agent) executeTool(ctx context.Context, tc message.ToolCall, events chan<- Event) message.ToolResult {
	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		return message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Output:     fmt.Sprintf("Error: unknown tool '%s'", tc.Name),
			IsError:    true,
		}
	}

	// Check mode restrictions
	if a.mode == "plan" && tool.RequiresPermission() {
		// In plan mode, block tools that modify files
		// Exception: bash for read-only commands (we can't really tell, so we block all bash in plan mode for safety)
		return message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Output:     fmt.Sprintf("Error: tool '%s' is not available in PLAN mode. Switch to BUILD mode to use this tool.", tc.Name),
			IsError:    true,
		}
	}

	// Check permissions for tools that require them
	if tool.RequiresPermission() && a.permSvc != nil {
		resp := a.permSvc.Check(ctx, tc.Name, string(tc.Input))
		if resp == permission.Deny {
			return message.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Output:     "Permission denied by user.",
				IsError:    true,
			}
		}
	}

	// Execute the tool
	output, err := tool.Execute(ctx, tc.Input)
	if err != nil {
		return message.ToolResult{
			ToolCallID: tc.ID,
			Name:       tc.Name,
			Output:     fmt.Sprintf("Error: %s", err.Error()),
			IsError:    true,
		}
	}

	return message.ToolResult{
		ToolCallID: tc.ID,
		Name:       tc.Name,
		Output:     output,
		IsError:    false,
	}
}

// buildToolDefs creates tool definitions, filtering by mode.
func (a *Agent) buildToolDefs() []provider.ToolDefinition {
	var defs []provider.ToolDefinition
	for _, t := range a.registry.All() {
		// In plan mode, skip tools that require permission (write tools)
		if a.mode == "plan" && t.RequiresPermission() {
			continue
		}
		defs = append(defs, provider.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}
