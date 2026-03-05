package planner

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nullism/goder/internal/llm/agent"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/tools"
)

type scriptedProvider struct {
	name      string
	responses []string

	mu       sync.Mutex
	calls    int
	requests []provider.Request
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	resp := ""
	if len(p.responses) > 0 {
		idx := call - 1
		if idx >= len(p.responses) {
			idx = len(p.responses) - 1
		}
		resp = p.responses[idx]
	}

	ch := make(chan provider.StreamEvent, 8)
	go func() {
		defer close(ch)
		if resp != "" {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: resp}
		}
		ch <- provider.StreamEvent{Type: provider.EventDone, Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
	}()

	return ch, nil
}

func (p *scriptedProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *scriptedProvider) SetAPIKey(_ string)                             {}
func (p *scriptedProvider) SetModel(_ string)                              {}

func (p *scriptedProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *scriptedProvider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

type readTool struct{}

func (t *readTool) Name() string                { return "glob" }
func (t *readTool) Description() string         { return "read-only tool" }
func (t *readTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *readTool) RequiresPermission() bool    { return false }
func (t *readTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

type writeTool struct{}

func (t *writeTool) Name() string                { return "write" }
func (t *writeTool) Description() string         { return "write tool" }
func (t *writeTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *writeTool) RequiresPermission() bool    { return true }
func (t *writeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "ok", nil
}

func newTestRegistry() *tools.Registry {
	r := tools.NewRegistry()
	r.Register(&readTool{})
	r.Register(&writeTool{})
	return r
}

func TestPlannerRun_ApproveFirstRound(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"Draft v1", "Final summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nLooks good."}}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 3,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Add a feature")}
	events := p.Run(context.Background(), history, "sess-1")

	var (
		finalMsg *message.Message
		gotErr   bool
	)
	for ev := range events {
		switch ev.Type {
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotErr = true
		}
	}

	if gotErr {
		t.Fatal("expected successful reviewed planning flow")
	}
	if finalMsg == nil {
		t.Fatal("expected final message")
	}
	if finalMsg.Content != "Final summary" {
		t.Fatalf("expected summary content, got %q", finalMsg.Content)
	}
	if mainProv.Calls() != 2 {
		t.Fatalf("expected 2 main calls (draft + summary), got %d", mainProv.Calls())
	}
	if reviewProv.Calls() != 1 {
		t.Fatalf("expected 1 reviewer call, got %d", reviewProv.Calls())
	}
}

func TestPlannerRun_ReviseThenApprove(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"Draft v1", "Draft v2", "Final summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{
		"VERDICT: REVISE\n\nNeed better migration details.",
		"VERDICT: APPROVE\n\nNow good.",
	}}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 3,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Refactor config flow")}
	events := p.Run(context.Background(), history, "sess-1")
	for range events {
	}

	if mainProv.Calls() != 3 {
		t.Fatalf("expected 3 main calls (2 drafts + summary), got %d", mainProv.Calls())
	}
	if reviewProv.Calls() != 2 {
		t.Fatalf("expected 2 reviewer calls, got %d", reviewProv.Calls())
	}

	// Call 2 from main provider should be the second draft generation.
	mainReqs := mainProv.Requests()
	if len(mainReqs) < 2 {
		t.Fatalf("expected at least 2 main requests, got %d", len(mainReqs))
	}
	lastMsg := mainReqs[1].Messages[len(mainReqs[1].Messages)-1].Content
	if !strings.Contains(lastMsg, "Need better migration details") {
		t.Fatalf("expected reviewer feedback to be included in round-2 draft input, got %q", lastMsg)
	}
}

func TestPlannerRun_ExhaustedRoundsStillSummarizes(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"Draft v1", "Draft v2", "Best effort summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{
		"VERDICT: REVISE\n\nIssue one.",
		"VERDICT: REVISE\n\nIssue two.",
	}}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 2,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Implement endpoint")}
	events := p.Run(context.Background(), history, "sess-1")

	var (
		finalMsg *message.Message
		gotErr   bool
	)
	for ev := range events {
		switch ev.Type {
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotErr = true
		}
	}

	if gotErr {
		t.Fatal("expected best-effort summary, not hard failure")
	}
	if finalMsg == nil || finalMsg.Content != "Best effort summary" {
		t.Fatalf("expected best-effort summary final message, got %#v", finalMsg)
	}
}

func TestPlannerRun_ReviewerUsesReadOnlyTools(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"Draft", "Summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nok"}}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 1,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Check tool access")}
	events := p.Run(context.Background(), history, "sess-1")
	for range events {
	}

	req := reviewProv.Requests()[0]
	if len(req.Tools) == 0 {
		t.Fatal("expected reviewer to receive read-only tools")
	}
	for _, td := range req.Tools {
		if td.Name == "write" {
			t.Fatal("reviewer should not receive permissioned write tool")
		}
	}
}

func TestPlannerRun_MissingReviewerConfig(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"unused"}}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: nil,
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 1,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Do work")}
	events := p.Run(context.Background(), history, "sess-1")

	var gotErr bool
	for ev := range events {
		if ev.Type == agent.EventAgentError {
			gotErr = true
		}
	}

	if !gotErr {
		t.Fatal("expected error when reviewer is not configured")
	}
}

func TestParseVerdict(t *testing.T) {
	if v := parseVerdict("VERDICT: APPROVE\n\nLooks good"); v != verdictApprove {
		t.Fatalf("expected APPROVE, got %q", v)
	}
	if v := parseVerdict("VERDICT: REVISE\n\nFix X"); v != verdictRevise {
		t.Fatalf("expected REVISE, got %q", v)
	}
	if v := parseVerdict("No explicit verdict"); v != verdictRevise {
		t.Fatalf("expected default REVISE, got %q", v)
	}
}
