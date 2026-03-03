# Fix: Planning Agents Missing Conversation Context

## Problem

Planning agents only receive the last user message, not prior conversation history. When a user's request references earlier context (e.g., "I want them to appear below tokens"), planners have no idea what "them" refers to and produce confused or useless plans.

**Root cause:** `dispatchPlanners()` in `internal/llm/planner/planner.go:155-161` extracts only the last user message text and passes it as a single string to `agent.RunSync()`, which creates a brand new single-message history.

The same issue affects the synthesizer at lines 237-242.

## Solution

Pass a **filtered conversation history** to planners and the synthesizer. The filter strips out tool calls, tool results, and tool-role messages, keeping only user and assistant text content. This preserves the conversational flow (what was discussed, what decisions were made) without the bulk of file contents, grep results, and command outputs.

## Changes

### 1. `internal/llm/agent/agent.go` - Add `RunSyncWithHistory`

Add a new method that accepts a `[]message.Message` instead of a single task prompt string. Refactor `RunSync` to delegate to it.

**At line 121, replace the existing `RunSync` with:**

```go
// RunSync executes the agent loop synchronously and returns the final text
// output. It builds a minimal history from taskPrompt and runs until the
// agent produces a final response. Tool call/result events are consumed
// internally. This is intended for child agents within the orchestrator.
func (a *Agent) RunSync(ctx context.Context, taskPrompt string, sessionID string) (string, error) {
	history := []message.Message{
		message.NewUserMessage(sessionID, taskPrompt),
	}
	return a.RunSyncWithHistory(ctx, history, sessionID)
}

// RunSyncWithHistory is like RunSync but accepts a pre-built message history
// instead of a single task prompt. This allows callers (e.g. the planner) to
// pass conversation context to child agents.
func (a *Agent) RunSyncWithHistory(ctx context.Context, history []message.Message, sessionID string) (string, error) {
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
```

### 2. `internal/llm/planner/planner.go` - Add `filterHistoryForContext`

Add a helper function that strips the history down to just conversational text. This goes at the bottom of the file (after `synthesize`):

```go
// filterHistoryForContext creates a lightweight version of the conversation
// history suitable for giving planning agents conversational context. It
// keeps only user and assistant messages with text content, stripping out
// tool calls, tool results, and tool-role messages. This preserves the
// conversational flow without the bulk of file contents and command outputs.
func filterHistoryForContext(history []message.Message) []message.Message {
	var filtered []message.Message
	for _, msg := range history {
		switch msg.Role {
		case message.User:
			filtered = append(filtered, message.Message{
				ID:        msg.ID,
				SessionID: msg.SessionID,
				Role:      msg.Role,
				Content:   msg.Content,
				CreatedAt: msg.CreatedAt,
			})
		case message.Assistant:
			// Only include assistant messages that have text content
			// (skip pure tool-call-only messages with no text).
			if msg.Content != "" {
				filtered = append(filtered, message.Message{
					ID:        msg.ID,
					SessionID: msg.SessionID,
					Role:      msg.Role,
					Content:   msg.Content,
					CreatedAt: msg.CreatedAt,
				})
			}
		// Skip tool-role messages and system messages entirely
		}
	}
	return filtered
}
```

### 3. `internal/llm/planner/planner.go` - Update `dispatchPlanners`

**Replace lines 154-161** (the taskPrompt extraction block):

```go
	// Extract the last user message to use as the task prompt
	var taskPrompt string
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == message.User {
			taskPrompt = history[i].Content
			break
		}
	}
```

**With:**

```go
	// Build a filtered history with conversational context (user + assistant
	// text only, no tool calls/results) so planners understand references
	// to earlier parts of the conversation.
	contextHistory := filterHistoryForContext(history)
```

**Replace line 191:**

```go
			planText, err := ag.RunSync(planCtx, taskPrompt, sessionID)
```

**With:**

```go
			planText, err := ag.RunSyncWithHistory(planCtx, contextHistory, sessionID)
```

### 4. `internal/llm/planner/planner.go` - Update `synthesize`

**Replace lines 234-242** (the synthesis history construction that only includes the last user message):

```go
	// Build synthesis history: original user message + planner outputs
	var synthHistory []message.Message
	// Include the last user message from the original history
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == message.User {
			synthHistory = append(synthHistory, history[i])
			break
		}
	}
```

**With:**

```go
	// Build synthesis history: filtered conversation context + planner outputs.
	// Include the full conversational context (user + assistant text) so the
	// synthesizer understands what was discussed earlier.
	synthHistory := filterHistoryForContext(history)
```

The rest of `synthesize` (appending the plans as a user message at line 244) remains unchanged.

## Verification

1. `go build ./...` should compile cleanly
2. `go test ./internal/llm/...` should pass
3. Manual test: Start a multi-turn conversation, then trigger planning mode with a message that references earlier context. The planners should now understand the reference.

## Risks

- **Token usage increase:** The filtered history adds conversational context to each planner's input. For long conversations with many back-and-forth exchanges, this could be significant. However, since tool outputs (the largest messages) are stripped, this should be manageable in practice.
- **No hard cap:** There's no limit on how much filtered history is included. A very long conversation could still exceed context windows. A future enhancement could add a token budget or sliding window, but that's a separate concern.
