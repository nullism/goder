package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/tools"
)

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	calls    int
	maxCalls int // after this many calls with tools, respond with tool calls; otherwise text only
}

func (m *mockProvider) Name() string { return "mock" }

func (m *mockProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	m.calls++
	ch := make(chan provider.StreamEvent, 10)

	go func() {
		defer close(ch)

		if m.calls <= m.maxCalls && len(req.Tools) > 0 {
			// Respond with a tool call to keep the loop going
			ch <- provider.StreamEvent{
				Type:         provider.EventToolCallStart,
				ToolCallID:   "call-1",
				ToolCallName: "glob",
			}
			ch <- provider.StreamEvent{
				Type:          provider.EventToolCallEnd,
				ToolCallID:    "call-1",
				ToolCallInput: `{"pattern":"*.go"}`,
			}
		} else {
			// Respond with text only (either naturally or because no tools provided)
			ch <- provider.StreamEvent{
				Type: provider.EventTextDelta,
				Text: "Here is a summary of progress.",
			}
		}
		ch <- provider.StreamEvent{
			Type:  provider.EventDone,
			Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}
	}()

	return ch, nil
}

func (m *mockProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (m *mockProvider) SetAPIKey(_ string)                             {}
func (m *mockProvider) SetModel(_ string)                              {}

// mockTool implements tools.Tool for testing.
type mockTool struct{}

func (t *mockTool) Name() string                { return "glob" }
func (t *mockTool) Description() string         { return "test tool" }
func (t *mockTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *mockTool) RequiresPermission() bool    { return false }
func (t *mockTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "file.go", nil
}

func TestMaxIterationsProducesFinalSummary(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&mockTool{})

	mp := &mockProvider{maxCalls: 100} // always return tool calls when tools are available

	ag := New(Config{
		Provider:      mp,
		Registry:      registry,
		Mode:          "build",
		MaxIterations: 3,
	})

	ctx := context.Background()
	eventCh := ag.Run(ctx, []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}, "sess-1")

	var gotDone bool
	var gotError bool
	var finalText string

	for ev := range eventCh {
		switch ev.Type {
		case EventAgentDone:
			gotDone = true
			if ev.FinalMessage != nil {
				finalText = ev.FinalMessage.Content
			}
		case EventAgentError:
			gotError = true
			t.Logf("unexpected error: %v", ev.Error)
		}
	}

	if gotError {
		t.Fatal("expected EventAgentDone after max iterations, got EventAgentError")
	}
	if !gotDone {
		t.Fatal("expected EventAgentDone event")
	}
	if finalText == "" {
		t.Error("expected non-empty final summary text")
	}

	// The mock provider should have been called maxIterations + 1 times:
	// 3 iterations with tool calls + 1 final summary call without tools
	if mp.calls != 4 {
		t.Errorf("expected 4 provider calls (3 iterations + 1 summary), got %d", mp.calls)
	}
}

func TestNormalCompletionBeforeMaxIterations(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&mockTool{})

	// Provider returns tool calls for the first 2 calls, then text only
	mp := &mockProvider{maxCalls: 2}

	ag := New(Config{
		Provider:      mp,
		Registry:      registry,
		Mode:          "build",
		MaxIterations: 10,
	})

	ctx := context.Background()
	eventCh := ag.Run(ctx, []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}, "sess-1")

	var gotDone bool
	var gotError bool

	for ev := range eventCh {
		switch ev.Type {
		case EventAgentDone:
			gotDone = true
		case EventAgentError:
			gotError = true
		}
	}

	if gotError {
		t.Fatal("expected normal completion, got error")
	}
	if !gotDone {
		t.Fatal("expected EventAgentDone event")
	}

	// 2 tool-call iterations + 1 final text response = 3 calls
	if mp.calls != 3 {
		t.Errorf("expected 3 provider calls, got %d", mp.calls)
	}
}

func TestMaxIterationsFinalCallHasNoTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&mockTool{})

	var lastReqTools []provider.ToolDefinition

	// Custom provider that records the tools on each request
	mp := &recordingProvider{
		maxCalls:  100,
		onRequest: func(req provider.Request) { lastReqTools = req.Tools },
	}

	ag := New(Config{
		Provider:      mp,
		Registry:      registry,
		Mode:          "build",
		MaxIterations: 2,
	})

	ctx := context.Background()
	eventCh := ag.Run(ctx, []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}, "sess-1")

	for range eventCh {
		// drain events
	}

	// The final call (summary) should have had no tools
	if lastReqTools != nil {
		t.Errorf("expected final summary request to have nil tools, got %d tools", len(lastReqTools))
	}
}

// recordingProvider is a mock provider that records request details.
type recordingProvider struct {
	calls     int
	maxCalls  int
	onRequest func(req provider.Request)
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.calls++
	if p.onRequest != nil {
		p.onRequest(req)
	}

	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)

		if p.calls <= p.maxCalls && len(req.Tools) > 0 {
			ch <- provider.StreamEvent{
				Type:         provider.EventToolCallStart,
				ToolCallID:   "call-1",
				ToolCallName: "glob",
			}
			ch <- provider.StreamEvent{
				Type:          provider.EventToolCallEnd,
				ToolCallID:    "call-1",
				ToolCallInput: `{"pattern":"*.go"}`,
			}
		} else {
			ch <- provider.StreamEvent{
				Type: provider.EventTextDelta,
				Text: "Summary text.",
			}
		}
		ch <- provider.StreamEvent{
			Type:  provider.EventDone,
			Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}
	}()

	return ch, nil
}

func (p *recordingProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *recordingProvider) SetAPIKey(_ string)                             {}
func (p *recordingProvider) SetModel(_ string)                              {}

func TestSanitizeHistory_EmptyHistory(t *testing.T) {
	result := sanitizeHistory(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d messages", len(result))
	}
}

func TestSanitizeHistory_NoOrphanedToolCalls(t *testing.T) {
	history := []message.Message{
		message.NewUserMessage("s1", "hello"),
		message.NewAssistantMessage("s1", "hi there", nil),
	}
	result := sanitizeHistory(history)
	if len(result) != 2 {
		t.Errorf("expected 2 messages (unchanged), got %d", len(result))
	}
}

func TestSanitizeHistory_ValidToolCallResultPair(t *testing.T) {
	toolCalls := []message.ToolCall{
		{ID: "tc-1", Name: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
	}
	toolResults := []message.ToolResult{
		{ToolCallID: "tc-1", Name: "glob", Output: "file.go"},
	}
	history := []message.Message{
		message.NewUserMessage("s1", "hello"),
		message.NewAssistantMessage("s1", "", toolCalls),
		message.NewToolResultMessage("s1", toolResults),
	}
	result := sanitizeHistory(history)
	if len(result) != 3 {
		t.Errorf("expected 3 messages (unchanged), got %d", len(result))
	}
}

func TestSanitizeHistory_OrphanedAssistantWithToolCalls(t *testing.T) {
	toolCalls := []message.ToolCall{
		{ID: "tc-1", Name: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
		{ID: "tc-2", Name: "grep", Input: json.RawMessage(`{"pattern":"foo"}`)},
	}
	history := []message.Message{
		message.NewUserMessage("s1", "hello"),
		message.NewAssistantMessage("s1", "Let me search", toolCalls),
	}

	result := sanitizeHistory(history)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (original 2 + synthetic tool result), got %d", len(result))
	}

	stubMsg := result[2]
	if stubMsg.Role != message.Tool {
		t.Errorf("expected synthetic message role to be Tool, got %s", stubMsg.Role)
	}
	if len(stubMsg.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(stubMsg.ToolResults))
	}
	if stubMsg.ToolResults[0].ToolCallID != "tc-1" {
		t.Errorf("expected first tool result to reference tc-1, got %s", stubMsg.ToolResults[0].ToolCallID)
	}
	if stubMsg.ToolResults[1].ToolCallID != "tc-2" {
		t.Errorf("expected second tool result to reference tc-2, got %s", stubMsg.ToolResults[1].ToolCallID)
	}
	for i, tr := range stubMsg.ToolResults {
		if !tr.IsError {
			t.Errorf("expected tool result %d to be an error", i)
		}
		if tr.Output != "Tool execution was interrupted." {
			t.Errorf("expected stub output, got %q", tr.Output)
		}
	}
}

func TestSanitizeHistory_AssistantWithoutToolCalls(t *testing.T) {
	// An assistant message without tool calls at the end is fine.
	history := []message.Message{
		message.NewUserMessage("s1", "hello"),
		message.NewAssistantMessage("s1", "here is your answer", nil),
	}
	result := sanitizeHistory(history)
	if len(result) != 2 {
		t.Errorf("expected 2 messages (unchanged), got %d", len(result))
	}
}

func TestCancellationPersistsBothMessages(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&mockTool{})

	mp := &mockProvider{maxCalls: 100}

	// Use a provider that cancels the context after the first LLM response,
	// so the agent sees cancellation at the start of the second iteration.
	// This is done synchronously in the agent goroutine (via the wrapped
	// channel), avoiding the race between consumer and producer goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	wrappingProvider := &cancellingMockProvider{
		inner:    mp,
		cancelFn: cancel,
	}

	ag := New(Config{
		Provider:      wrappingProvider,
		Registry:      registry,
		Mode:          "build",
		MaxIterations: 10,
	})

	eventCh := ag.Run(ctx, []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}, "sess-1")

	var persistedMessages []*message.Message
	var gotError bool

	for ev := range eventCh {
		switch ev.Type {
		case EventPersistMessage:
			persistedMessages = append(persistedMessages, ev.FinalMessage)
		case EventAgentError:
			gotError = true
		}
	}

	if !gotError {
		t.Fatal("expected EventAgentError after cancellation")
	}

	// We should have exactly one pair of persist messages from the first
	// iteration (assistant + tool result). The second iteration fails at
	// the ctx.Err() check or SendMessage, so no more persist events.
	if len(persistedMessages) == 0 {
		t.Fatal("expected at least one pair of persisted messages")
	}
	if len(persistedMessages)%2 != 0 {
		t.Errorf("expected persisted messages to come in pairs, got %d messages", len(persistedMessages))
	}

	// Verify the pairing: each pair should be (assistant with tool calls, tool result).
	for i := 0; i+1 < len(persistedMessages); i += 2 {
		assistant := persistedMessages[i]
		toolResult := persistedMessages[i+1]

		if assistant.Role != message.Assistant {
			t.Errorf("pair %d: expected assistant message, got %s", i/2, assistant.Role)
		}
		if len(assistant.ToolCalls) == 0 {
			t.Errorf("pair %d: expected assistant message to have tool calls", i/2)
		}
		if toolResult.Role != message.Tool {
			t.Errorf("pair %d: expected tool result message, got %s", i/2, toolResult.Role)
		}
		if len(toolResult.ToolResults) == 0 {
			t.Errorf("pair %d: expected tool result message to have results", i/2)
		}

		// Verify tool call IDs match between assistant and tool result.
		callIDs := make(map[string]bool)
		for _, tc := range assistant.ToolCalls {
			callIDs[tc.ID] = true
		}
		for _, tr := range toolResult.ToolResults {
			if !callIDs[tr.ToolCallID] {
				t.Errorf("pair %d: tool result references unknown tool call ID %s", i/2, tr.ToolCallID)
			}
		}
	}
}

func TestCancellationDuringToolExecution(t *testing.T) {
	// This tests the case where context is cancelled BEFORE any tool
	// in a batch executes (cancelled between LLM response and tool execution).
	registry := tools.NewRegistry()
	registry.Register(&mockTool{})

	mp := &mockProvider{maxCalls: 100}

	// Use a provider that cancels the context after the LLM response
	// is fully consumed, so the agent sees cancellation before tool execution.
	ctx, cancel := context.WithCancel(context.Background())
	cancellingProvider := &cancellingMockProvider{
		inner:    mp,
		cancelFn: cancel,
	}

	ag := New(Config{
		Provider:      cancellingProvider,
		Registry:      registry,
		Mode:          "build",
		MaxIterations: 10,
	})

	eventCh := ag.Run(ctx, []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}, "sess-1")

	var persistedMessages []*message.Message
	var gotError bool

	for ev := range eventCh {
		switch ev.Type {
		case EventPersistMessage:
			persistedMessages = append(persistedMessages, ev.FinalMessage)
		case EventAgentError:
			gotError = true
		}
	}

	if !gotError {
		t.Fatal("expected EventAgentError after cancellation")
	}

	// Even though cancellation happened before tool execution, we should
	// still get a pair of persist messages with stub tool results.
	if len(persistedMessages) != 2 {
		t.Fatalf("expected exactly 2 persisted messages (assistant + stub tool result), got %d", len(persistedMessages))
	}

	assistant := persistedMessages[0]
	toolResult := persistedMessages[1]

	if assistant.Role != message.Assistant || len(assistant.ToolCalls) == 0 {
		t.Error("expected assistant message with tool calls")
	}
	if toolResult.Role != message.Tool || len(toolResult.ToolResults) == 0 {
		t.Error("expected tool result message with results")
	}

	// All results should be stubs (errors with "interrupted" message).
	for _, tr := range toolResult.ToolResults {
		if !tr.IsError {
			t.Error("expected stub tool result to be an error")
		}
		if tr.Output != "Tool execution was interrupted." {
			t.Errorf("expected stub output, got %q", tr.Output)
		}
	}
}

// cancellingMockProvider wraps a provider and cancels the context after
// the first successful SendMessage call returns tool calls.
type cancellingMockProvider struct {
	inner    provider.Provider
	cancelFn context.CancelFunc
	called   bool
}

func (p *cancellingMockProvider) Name() string { return "cancelling" }

func (p *cancellingMockProvider) SendMessage(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	ch, err := p.inner.SendMessage(ctx, req)
	if err != nil {
		return nil, err
	}

	if !p.called {
		p.called = true
		// Wrap the channel to cancel context after all events are sent
		wrappedCh := make(chan provider.StreamEvent, 64)
		go func() {
			defer close(wrappedCh)
			for ev := range ch {
				wrappedCh <- ev
			}
			// Cancel context after the LLM response is fully consumed,
			// so the agent sees cancellation when it checks before tool execution.
			p.cancelFn()
		}()
		return wrappedCh, nil
	}

	return ch, nil
}

func (p *cancellingMockProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *cancellingMockProvider) SetAPIKey(_ string)                             {}
func (p *cancellingMockProvider) SetModel(_ string)                              {}

// contextAwareMockProvider is like mockProvider but checks context cancellation
// before returning from SendMessage, ensuring the agent loop terminates promptly.
type contextAwareMockProvider struct {
	calls    int
	maxCalls int
}

func (m *contextAwareMockProvider) Name() string { return "context-aware-mock" }

func (m *contextAwareMockProvider) SendMessage(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	// Check context before proceeding — this is what real providers do.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	m.calls++
	ch := make(chan provider.StreamEvent, 10)

	go func() {
		defer close(ch)

		if m.calls <= m.maxCalls && len(req.Tools) > 0 {
			ch <- provider.StreamEvent{
				Type:         provider.EventToolCallStart,
				ToolCallID:   "call-1",
				ToolCallName: "glob",
			}
			ch <- provider.StreamEvent{
				Type:          provider.EventToolCallEnd,
				ToolCallID:    "call-1",
				ToolCallInput: `{"pattern":"*.go"}`,
			}
		} else {
			ch <- provider.StreamEvent{
				Type: provider.EventTextDelta,
				Text: "Summary text.",
			}
		}
		ch <- provider.StreamEvent{
			Type:  provider.EventDone,
			Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}
	}()

	return ch, nil
}

func (m *contextAwareMockProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (m *contextAwareMockProvider) SetAPIKey(_ string)                             {}
func (m *contextAwareMockProvider) SetModel(_ string)                              {}
