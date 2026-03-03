package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nullism/goder/internal/llm/prompt"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/permission"
	"github.com/nullism/goder/internal/tools"
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
	EventPlanningPhase  // planning phase status change
	EventPlannerStart   // a planning agent began working
	EventPlannerDone    // a planning agent finished
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

	// For orchestrator events
	PlanPhase    string // human-readable phase description (EventPlanningPhase)
	PlannerModel string // which model handled this planner (EventPlannerStart/Done)
	PlannerPlan  string // completed planner plan text (EventPlannerDone)
}

// Agent orchestrates the LLM + tool execution loop.
type Agent struct {
	provider      provider.Provider
	registry      *tools.Registry
	permSvc       *permission.Service
	workDir       string
	model         string
	maxTokens     int
	maxIterations int
	systemPrompt  string // if non-empty, overrides the default prompt builder
}

// Config holds agent construction parameters.
type Config struct {
	Provider      provider.Provider
	Registry      *tools.Registry
	PermSvc       *permission.Service
	WorkDir       string
	Model         string
	MaxTokens     int
	MaxIterations int
	SystemPrompt  string // if set, used instead of the default prompt builder
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
		model:         cfg.Model,
		maxTokens:     cfg.MaxTokens,
		maxIterations: maxIter,
		systemPrompt:  cfg.SystemPrompt,
	}
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

// RunSync executes the agent loop synchronously and returns the final text
// output. It builds a minimal history from taskPrompt and runs until the
// agent produces a final response. Tool call/result events are consumed
// internally. This is intended for child agents within the orchestrator.
func (a *Agent) RunSync(ctx context.Context, taskPrompt string, sessionID string) (string, error) {
	history := []message.Message{
		message.NewUserMessage(sessionID, taskPrompt),
	}

	events := a.Run(ctx, history, sessionID)

	var result strings.Builder
	for ev := range events {
		switch ev.Type {
		case EventStreamText:
			result.WriteString(ev.Text)
		case EventAgentDone:
			if ev.FinalMessage != nil {
				return ev.FinalMessage.Content, nil
			}
			return result.String(), nil
		case EventAgentError:
			return result.String(), ev.Error
		}
	}
	return result.String(), nil
}

func (a *Agent) runLoop(ctx context.Context, history []message.Message, sessionID string, events chan<- Event) {
	systemPrompt := a.systemPrompt
	if systemPrompt == "" {
		systemPrompt = prompt.BuildSystemPrompt(a.model, a.workDir, a.registry)
	}

	// Build tool definitions, filtering by mode
	toolDefs := a.buildToolDefs()

	currentHistory := make([]message.Message, len(history))
	copy(currentHistory, history)

	// Ensure the loaded history is structurally valid. If a previous run
	// was cancelled after persisting an assistant message with tool calls
	// but before persisting the tool results, the history would be invalid
	// for providers like Anthropic that require every tool_use to have a
	// corresponding tool_result.
	currentHistory = sanitizeHistory(currentHistory)

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

		// Execute tool calls. We defer persisting the assistant message
		// until after tool results are ready so that both are persisted
		// together. This prevents orphaned assistant messages (with tool
		// calls but no results) if the agent is cancelled mid-execution,
		// which would cause Anthropic to reject the conversation history.
		var toolResults []message.ToolResult
		cancelled := false
		for i, tc := range toolCalls {
			if ctx.Err() != nil {
				// Context cancelled — generate stub results for all
				// remaining tool calls so the history stays valid.
				for _, remaining := range toolCalls[i:] {
					toolResults = append(toolResults, message.ToolResult{
						ToolCallID: remaining.ID,
						Name:       remaining.Name,
						Output:     "Tool execution was interrupted.",
						IsError:    true,
					})
				}
				cancelled = true
				break
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

		// Persist both the assistant message and tool result message
		// together so they are never orphaned in the database.
		events <- Event{Type: EventPersistMessage, FinalMessage: &assistantMsg}
		events <- Event{Type: EventPersistMessage, FinalMessage: &toolResultMsg}

		if cancelled {
			events <- Event{Type: EventAgentError, Error: ctx.Err()}
			return
		}

		// Continue the loop - the LLM will see the tool results and respond
	}

	// Max iterations reached — make one final LLM call without tools so the
	// model can produce a summary of what was accomplished and what remains.
	wrapUpPrompt := systemPrompt + "\n\n" +
		"IMPORTANT: You have reached the maximum number of iterations (" +
		fmt.Sprintf("%d", a.maxIterations) +
		"). You can no longer call tools. Provide a concise summary of what you accomplished " +
		"and what remains to be done, so the user can continue in a follow-up message."

	req := provider.Request{
		SystemPrompt: wrapUpPrompt,
		Messages:     currentHistory,
		Tools:        nil, // no tools available
		MaxTokens:    a.maxTokens,
	}

	streamCh, err := a.provider.SendMessage(ctx, req)
	if err != nil {
		events <- Event{
			Type:  EventAgentError,
			Error: fmt.Errorf("final summary request failed after max iterations: %w", err),
		}
		return
	}

	var finalText strings.Builder
	var finalUsage provider.Usage

	for event := range streamCh {
		switch event.Type {
		case provider.EventTextDelta:
			finalText.WriteString(event.Text)
			events <- Event{Type: EventStreamText, Text: event.Text}
		case provider.EventError:
			events <- Event{Type: EventAgentError, Error: event.Error}
			return
		case provider.EventDone:
			finalUsage = event.Usage
		}
	}

	finalMsg := message.NewAssistantMessage(sessionID, finalText.String(), nil)
	finalMsg.InputTokens = finalUsage.InputTokens
	finalMsg.OutputTokens = finalUsage.OutputTokens
	finalMsg.TotalTokens = finalUsage.TotalTokens

	events <- Event{Type: EventAgentDone, FinalMessage: &finalMsg}
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

// buildToolDefs creates tool definitions from the registry.
func (a *Agent) buildToolDefs() []provider.ToolDefinition {
	var defs []provider.ToolDefinition
	for _, t := range a.registry.All() {
		defs = append(defs, provider.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Parameters(),
		})
	}
	return defs
}

// sanitizeHistory ensures the message history is structurally valid for LLM
// providers. It scans the entire history (not just the last message) and
// fixes the following issues:
//
//  1. Orphaned assistant messages: An assistant message with tool calls that
//     is not immediately followed by a tool result message gets a synthetic
//     stub tool result message inserted after it.
//
//  2. Mismatched tool results: A tool result message whose ToolCallIDs don't
//     match the preceding assistant message's ToolCall IDs is corrected —
//     orphaned results are dropped, and missing results get stubs.
//
//  3. Orphaned tool result messages: A tool result message that is not
//     preceded by an assistant message with tool calls is removed entirely.
//
// This prevents providers like Anthropic from rejecting the request with
// errors about unexpected tool_use_id in tool_result blocks.
func sanitizeHistory(history []message.Message) []message.Message {
	if len(history) == 0 {
		return history
	}

	var sanitized []message.Message

	for i := 0; i < len(history); i++ {
		msg := history[i]

		switch {
		case msg.Role == message.Assistant && len(msg.ToolCalls) > 0:
			// This assistant message has tool calls. Check the next message.
			sanitized = append(sanitized, msg)

			// Build a set of expected tool call IDs.
			expectedIDs := make(map[string]message.ToolCall)
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = tc
			}

			// Check if the next message is a matching tool result.
			if i+1 < len(history) && history[i+1].Role == message.Tool {
				next := history[i+1]
				i++ // consume the tool result message

				// Filter results to only those matching expected IDs,
				// and track which expected IDs were satisfied.
				var validResults []message.ToolResult
				for _, tr := range next.ToolResults {
					if _, ok := expectedIDs[tr.ToolCallID]; ok {
						validResults = append(validResults, tr)
						delete(expectedIDs, tr.ToolCallID)
					}
					// Drop results whose ToolCallID doesn't match any
					// tool call in the preceding assistant message.
				}

				// Synthesize stubs for any tool calls that had no result.
				for _, tc := range msg.ToolCalls {
					if _, missing := expectedIDs[tc.ID]; missing {
						validResults = append(validResults, message.ToolResult{
							ToolCallID: tc.ID,
							Name:       tc.Name,
							Output:     "Tool execution was interrupted.",
							IsError:    true,
						})
					}
				}

				fixedMsg := message.NewToolResultMessage(next.SessionID, validResults)
				fixedMsg.ID = next.ID
				fixedMsg.CreatedAt = next.CreatedAt
				sanitized = append(sanitized, fixedMsg)
			} else {
				// No tool result message follows — synthesize one entirely.
				var results []message.ToolResult
				for _, tc := range msg.ToolCalls {
					results = append(results, message.ToolResult{
						ToolCallID: tc.ID,
						Name:       tc.Name,
						Output:     "Tool execution was interrupted.",
						IsError:    true,
					})
				}
				stubMsg := message.NewToolResultMessage(msg.SessionID, results)
				sanitized = append(sanitized, stubMsg)
			}

		case msg.Role == message.Tool:
			// A tool result message without a preceding assistant message
			// with tool calls. This is orphaned — drop it.
			continue

		default:
			sanitized = append(sanitized, msg)
		}
	}

	return sanitized
}
