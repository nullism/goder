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
	autoTools bool

	mu        sync.Mutex
	calls     int
	textCalls int
	requests  []provider.Request
}

func (p *scriptedProvider) Name() string { return p.name }

func (p *scriptedProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	emitToolCall := p.autoTools && len(req.Tools) > 0 && !requestHasTrailingToolResult(req.Messages)
	var resp string
	if !emitToolCall {
		p.textCalls++
		idx := p.textCalls - 1
		if len(p.responses) > 0 {
			if idx >= len(p.responses) {
				idx = len(p.responses) - 1
			}
			resp = p.responses[idx]
		}
	}
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	ch := make(chan provider.StreamEvent, 8)
	go func() {
		defer close(ch)
		if emitToolCall {
			ch <- provider.StreamEvent{Type: provider.EventToolCallStart, ToolCallID: "call-1", ToolCallName: "glob"}
			ch <- provider.StreamEvent{Type: provider.EventToolCallEnd, ToolCallID: "call-1", ToolCallInput: `{"pattern":"**/*.go"}`}
		} else if resp != "" {
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

func (p *scriptedProvider) TextCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.textCalls
}

func (p *scriptedProvider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

func requestHasTrailingToolResult(msgs []message.Message) bool {
	if len(msgs) == 0 {
		return false
	}
	return msgs[len(msgs)-1].Role == message.Tool
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

const (
	complexDraftV1 = "Summary line\nStep one\nStep two"
	complexDraftV2 = "Updated summary\nStep one\nStep two"
)

func TestPlannerRun_ApproveFirstRound(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, "Final summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nLooks good."}, autoTools: true}
	mainProv.autoTools = true

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
	if mainProv.TextCalls() != 2 {
		t.Fatalf("expected 2 main text calls (draft + summary), got %d", mainProv.TextCalls())
	}
	if reviewProv.TextCalls() != 1 {
		t.Fatalf("expected 1 reviewer text call, got %d", reviewProv.TextCalls())
	}
}

func TestPlannerRun_ReviseThenApprove(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, complexDraftV2, "Final summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{
		"VERDICT: REVISE\n\nNeed better migration details.",
		"VERDICT: APPROVE\n\nNow good.",
	}, autoTools: true}
	mainProv.autoTools = true

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

	if mainProv.TextCalls() != 3 {
		t.Fatalf("expected 3 main text calls (2 drafts + summary), got %d", mainProv.TextCalls())
	}
	if reviewProv.TextCalls() != 2 {
		t.Fatalf("expected 2 reviewer text calls, got %d", reviewProv.TextCalls())
	}

	mainReqs := mainProv.Requests()
	if len(mainReqs) < 4 {
		t.Fatalf("expected at least 4 main requests with tool loops, got %d", len(mainReqs))
	}
	lastMsg := mainReqs[2].Messages[len(mainReqs[2].Messages)-1].Content
	if !strings.Contains(lastMsg, "Need better migration details") {
		t.Fatalf("expected reviewer feedback to be included in round-2 draft input, got %q", lastMsg)
	}
}

func TestPlannerRun_ExhaustedRoundsStillSummarizes(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, complexDraftV2, "Best effort summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{
		"VERDICT: REVISE\n\nIssue one.",
		"VERDICT: REVISE\n\nIssue two.",
	}, autoTools: true}
	mainProv.autoTools = true

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
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, "Summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nok"}, autoTools: true}
	mainProv.autoTools = true

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

	reqs := reviewProv.Requests()
	if len(reqs) == 0 {
		t.Fatal("expected reviewer requests")
	}
	req := reqs[0]
	if len(req.Tools) == 0 {
		t.Fatal("expected reviewer to receive read-only tools")
	}
	for _, td := range req.Tools {
		if td.Name == "write" {
			t.Fatal("reviewer should not receive permissioned write tool")
		}
	}
}

func TestPlannerRun_EmptyReviewerResponseRetriesAndSucceeds(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, "Final summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"", "VERDICT: APPROVE\n\nLooks good."}, autoTools: true}
	mainProv.autoTools = true

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 2,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Implement X")}
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
	if finalMsg == nil || finalMsg.Content != "Final summary" {
		t.Fatalf("expected final summary, got %#v", finalMsg)
	}
	if reviewProv.TextCalls() != 2 {
		t.Fatalf("expected 2 reviewer text calls (initial + retry), got %d", reviewProv.TextCalls())
	}

	reqs := reviewProv.Requests()
	if len(reqs) < 4 {
		t.Fatalf("expected at least 4 reviewer requests with tool loops, got %d", len(reqs))
	}
	if len(reqs[2].Tools) == 0 {
		t.Fatal("expected retry request to include read-only tools")
	}
	last := reqs[2].Messages[len(reqs[2].Messages)-1].Content
	if !strings.Contains(last, "had an issue (empty response)") {
		t.Fatalf("expected retry instruction in second reviewer request, got %q", last)
	}
}

func TestPlannerRun_EmptyReviewerResponseTwiceFallsBackToSyntheticRevise(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, complexDraftV2, "Best effort summary"}}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"", "", "VERDICT: APPROVE\n\nNow good."}, autoTools: true}
	mainProv.autoTools = true

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
		t.Fatal("expected synthetic fallback instead of hard failure")
	}
	if finalMsg == nil || finalMsg.Content != "Best effort summary" {
		t.Fatalf("expected best-effort summary final message, got %#v", finalMsg)
	}

	if reviewProv.TextCalls() != 3 {
		t.Fatalf("expected 3 reviewer text calls (2 in round 1, 1 in round 2), got %d", reviewProv.TextCalls())
	}

	mainReqs := mainProv.Requests()
	if len(mainReqs) < 4 {
		t.Fatalf("expected at least 4 main requests with tool loops, got %d", len(mainReqs))
	}
	round2Input := mainReqs[2].Messages[len(mainReqs[2].Messages)-1].Content
	if !strings.Contains(round2Input, "Reviewer validation was insufficient") {
		t.Fatalf("expected synthetic reviewer feedback in round-2 draft input, got %q", round2Input)
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

func TestPlannerRun_SimplePlanSkipsReviewer(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{"Quick patch plan.\nUpdate one file.", "Final summary"}, autoTools: true}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nunused"}, autoTools: true}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 3,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Tiny typo fix")}
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
		t.Fatal("expected simple-plan flow to succeed")
	}
	if finalMsg == nil || finalMsg.Content != "Final summary" {
		t.Fatalf("expected final summary, got %#v", finalMsg)
	}
	if reviewProv.Calls() != 0 {
		t.Fatalf("expected reviewer to be skipped for simple plan, got %d calls", reviewProv.Calls())
	}
}

func TestPlannerRun_MainWithoutInspectionFails(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1}, autoTools: false}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nunused"}, autoTools: true}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 2,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Add endpoint")}
	events := p.Run(context.Background(), history, "sess-1")

	gotErr := false
	for ev := range events {
		if ev.Type == agent.EventAgentError {
			gotErr = true
		}
	}

	if !gotErr {
		t.Fatal("expected planner error when main agent does not inspect repository")
	}
	if reviewProv.Calls() != 0 {
		t.Fatalf("expected reviewer not to run after main inspection failure, got %d calls", reviewProv.Calls())
	}
}

func TestPlannerRun_ReviewerWithoutInspectionTriggersSyntheticRevise(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, complexDraftV2, "Final summary"}, autoTools: true}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{
		"VERDICT: APPROVE\n\nNo repo evidence.",
		"VERDICT: APPROVE\n\nStill no repo evidence.",
	}, autoTools: false}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 2,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Refactor planner")}
	events := p.Run(context.Background(), history, "sess-1")
	for range events {
	}

	mainReqs := mainProv.Requests()
	if len(mainReqs) < 4 {
		t.Fatalf("expected at least 4 main requests with round-2 draft, got %d", len(mainReqs))
	}
	round2Input := mainReqs[2].Messages[len(mainReqs[2].Messages)-1].Content
	if !strings.Contains(round2Input, "missing repository inspection") {
		t.Fatalf("expected synthetic review reason to mention missing repository inspection, got %q", round2Input)
	}
}

func TestPlannerRun_ForwardsPlannerToolEvents(t *testing.T) {
	mainProv := &scriptedProvider{name: "main", responses: []string{complexDraftV1, "Final summary"}, autoTools: true}
	reviewProv := &scriptedProvider{name: "reviewer", responses: []string{"VERDICT: APPROVE\n\nLooks good."}, autoTools: true}

	p := New(Config{
		MainProvider: mainProv,
		ReviewerSpec: &ReviewerSpec{Provider: reviewProv, Model: "review-model"},
		Registry:     newTestRegistry(),
		WorkDir:      "/tmp/test",
		MainModel:    "main-model",
		ReviewRounds: 1,
	})

	history := []message.Message{message.NewUserMessage("sess-1", "Add feature")}
	events := p.Run(context.Background(), history, "sess-1")

	seen := map[string]bool{}
	for ev := range events {
		if ev.Type == agent.EventToolCallStart {
			seen[ev.ToolCallName] = true
		}
	}

	if !seen["main:glob"] {
		t.Fatal("expected forwarded main planner tool-call events")
	}
	if !seen["reviewer:glob"] {
		t.Fatal("expected forwarded reviewer planner tool-call events")
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
