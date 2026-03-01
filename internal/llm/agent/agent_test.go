package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/webgovernor/goder/internal/llm/provider"
	"github.com/webgovernor/goder/internal/message"
	"github.com/webgovernor/goder/internal/tools"
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
