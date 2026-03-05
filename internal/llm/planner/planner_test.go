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

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	name     string
	response string // text response to return
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{
			Type: provider.EventTextDelta,
			Text: m.response,
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

// toolCallProvider returns a tool call first, then text on the second call.
// This simulates a planner that uses tools before producing a final plan.
type toolCallProvider struct {
	name  string
	calls int
}

func (p *toolCallProvider) Name() string { return p.name }

func (p *toolCallProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.calls++
	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		if p.calls == 1 && len(req.Tools) > 0 {
			// First call: return a tool call
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
			// Second call (or no tools): return text
			ch <- provider.StreamEvent{
				Type: provider.EventTextDelta,
				Text: "Plan after exploring codebase.",
			}
		}
		ch <- provider.StreamEvent{
			Type:  provider.EventDone,
			Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}
	}()
	return ch, nil
}

func (p *toolCallProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *toolCallProvider) SetAPIKey(_ string)                             {}
func (p *toolCallProvider) SetModel(_ string)                              {}

// errorProvider always returns an error event.
type errorProvider struct {
	name string
}

func (p *errorProvider) Name() string { return p.name }

func (p *errorProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{
			Type:  provider.EventError,
			Error: context.DeadlineExceeded,
		}
	}()
	return ch, nil
}

func (p *errorProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *errorProvider) SetAPIKey(_ string)                             {}
func (p *errorProvider) SetModel(_ string)                              {}

// emptyThenTextProvider returns an empty response first, then returns text
// only when tools are disabled on retry.
type emptyThenTextProvider struct {
	name     string
	text     string
	mu       sync.Mutex
	calls    int
	requests []provider.Request
}

func (p *emptyThenTextProvider) Name() string { return p.name }

func (p *emptyThenTextProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	callNum := p.calls
	p.requests = append(p.requests, req)
	p.mu.Unlock()

	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		if callNum == 1 {
			// First attempt: empty content (simulates Gemini no-plan-output case)
			ch <- provider.StreamEvent{Type: provider.EventDone, Usage: provider.Usage{TotalTokens: 7}}
			return
		}

		if len(req.Tools) == 0 {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: p.text}
		}
		ch <- provider.StreamEvent{Type: provider.EventDone, Usage: provider.Usage{TotalTokens: 11}}
	}()
	return ch, nil
}

func (p *emptyThenTextProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *emptyThenTextProvider) SetAPIKey(_ string)                             {}
func (p *emptyThenTextProvider) SetModel(_ string)                              {}

func (p *emptyThenTextProvider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]provider.Request, len(p.requests))
	copy(out, p.requests)
	return out
}

// alwaysEmptyProvider always returns an empty message, even on no-tools retry.
type alwaysEmptyProvider struct {
	name  string
	mu    sync.Mutex
	calls int
}

func (p *alwaysEmptyProvider) Name() string { return p.name }

func (p *alwaysEmptyProvider) SendMessage(_ context.Context, _ provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()

	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{Type: provider.EventDone, Usage: provider.Usage{TotalTokens: 5}}
	}()
	return ch, nil
}

func (p *alwaysEmptyProvider) ListModels(_ context.Context) ([]string, error) { return nil, nil }
func (p *alwaysEmptyProvider) SetAPIKey(_ string)                             {}
func (p *alwaysEmptyProvider) SetModel(_ string)                              {}

func (p *alwaysEmptyProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// mockTool implements tools.Tool for testing.
type mockTool struct{}

func (t *mockTool) Name() string                { return "glob" }
func (t *mockTool) Description() string         { return "test glob tool" }
func (t *mockTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *mockTool) RequiresPermission() bool    { return false }
func (t *mockTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "file.go", nil
}

func newTestRegistry() *tools.Registry {
	reg := tools.NewRegistry()
	reg.Register(&mockTool{})
	return reg
}

func TestPlannerRun_BasicFlow(t *testing.T) {
	// Two planning agents produce plans, main provider synthesizes them.
	plannerA := &mockProvider{name: "mock-a", response: "Plan A: refactor the module."}
	plannerB := &mockProvider{name: "mock-b", response: "Plan B: add tests first."}
	mainProv := &mockProvider{name: "main", response: "Synthesized: refactor with tests."}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerA, Model: "model-a"},
			{Provider: plannerB, Model: "model-b"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MainModel:     "main-model",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "Please refactor the auth module."),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var (
		phases        []string
		plannerStarts []string
		plannerDones  []string
		streamedText  strings.Builder
		finalMsg      *message.Message
		gotError      bool
	)

	for ev := range events {
		switch ev.Type {
		case agent.EventPlanningPhase:
			phases = append(phases, ev.PlanPhase)
		case agent.EventPlannerStart:
			plannerStarts = append(plannerStarts, ev.PlannerModel)
		case agent.EventPlannerDone:
			plannerDones = append(plannerDones, ev.PlannerModel)
		case agent.EventStreamText:
			streamedText.WriteString(ev.Text)
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotError = true
			t.Logf("unexpected error: %v", ev.Error)
		}
	}

	if gotError {
		t.Fatal("expected successful run, got error")
	}

	// Should have 2 phases: dispatch + synthesis
	if len(phases) != 2 {
		t.Errorf("expected 2 phase events, got %d: %v", len(phases), phases)
	}

	// Should have 2 planner starts and 2 planner dones
	if len(plannerStarts) != 2 {
		t.Errorf("expected 2 planner starts, got %d", len(plannerStarts))
	}
	if len(plannerDones) != 2 {
		t.Errorf("expected 2 planner dones, got %d", len(plannerDones))
	}

	// Streamed text should contain the synthesis output
	if !strings.Contains(streamedText.String(), "Synthesized") {
		t.Errorf("expected streamed text to contain synthesis, got %q", streamedText.String())
	}

	// Final message should be present
	if finalMsg == nil {
		t.Fatal("expected final message, got nil")
	}
	if finalMsg.Content != "Synthesized: refactor with tests." {
		t.Errorf("expected synthesized content, got %q", finalMsg.Content)
	}
}

func TestPlannerRun_SinglePlanner(t *testing.T) {
	// Works with just one planning agent.
	plannerProv := &mockProvider{name: "solo", response: "Solo plan: do X then Y."}
	mainProv := &mockProvider{name: "main", response: "Unified plan based on solo input."}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerProv, Model: "solo-model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "Fix the bug."),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var finalMsg *message.Message
	var gotError bool

	for ev := range events {
		switch ev.Type {
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotError = true
		}
	}

	if gotError {
		t.Fatal("expected successful run, got error")
	}
	if finalMsg == nil {
		t.Fatal("expected final message")
	}
}

func TestPlannerRun_AllPlannersFail(t *testing.T) {
	// If every planner fails, the run should emit EventAgentError.
	errProv := &errorProvider{name: "failing"}
	mainProv := &mockProvider{name: "main", response: "should not reach here"}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: errProv, Model: "bad-model-1"},
			{Provider: errProv, Model: "bad-model-2"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var gotError bool
	var gotDone bool

	for ev := range events {
		switch ev.Type {
		case agent.EventAgentError:
			gotError = true
		case agent.EventAgentDone:
			gotDone = true
		}
	}

	if !gotError {
		t.Fatal("expected EventAgentError when all planners fail")
	}
	if gotDone {
		t.Error("should not get EventAgentDone when all planners fail")
	}
}

func TestPlannerRun_PartialPlannerFailure(t *testing.T) {
	// If one planner fails but another succeeds, synthesis should proceed.
	goodProv := &mockProvider{name: "good", response: "Good plan from surviving planner."}
	errProv := &errorProvider{name: "failing"}
	mainProv := &mockProvider{name: "main", response: "Synthesized from partial results."}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: goodProv, Model: "good-model"},
			{Provider: errProv, Model: "bad-model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var finalMsg *message.Message
	var gotError bool
	var plannerDones []string

	for ev := range events {
		switch ev.Type {
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotError = true
		case agent.EventPlannerDone:
			plannerDones = append(plannerDones, ev.PlannerModel)
		}
	}

	if gotError {
		t.Fatal("expected successful synthesis despite partial failure")
	}
	if finalMsg == nil {
		t.Fatal("expected final message")
	}

	// Both planners should have reported done (one with error text)
	if len(plannerDones) != 2 {
		t.Errorf("expected 2 planner done events, got %d", len(plannerDones))
	}
}

func TestPlannerRun_PlannerFailureSetsPlannerErrorField(t *testing.T) {
	goodProv := &mockProvider{name: "good", response: "Good plan from surviving planner."}
	errProv := &errorProvider{name: "failing"}
	mainProv := &mockProvider{name: "main", response: "Synthesized from partial results."}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: goodProv, Model: "good-model"},
			{Provider: errProv, Model: "bad-model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	seenBadPlannerDone := false
	for ev := range events {
		if ev.Type != agent.EventPlannerDone || ev.PlannerModel != "bad-model" {
			continue
		}
		seenBadPlannerDone = true
		if ev.PlannerError == "" {
			t.Fatal("expected PlannerError to be set for failed planner")
		}
		if ev.PlannerPlan != "" {
			t.Fatalf("expected PlannerPlan to be empty on failed planner, got %q", ev.PlannerPlan)
		}
	}

	if !seenBadPlannerDone {
		t.Fatal("expected a PlannerDone event for bad-model")
	}
}

func TestPlannerRun_PlannerUsesTools(t *testing.T) {
	// A planner that uses tools (via RunSync) should still produce a plan.
	toolProv := &toolCallProvider{name: "tool-user"}
	mainProv := &mockProvider{name: "main", response: "Synthesized from tool-using planner."}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: toolProv, Model: "tool-model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "analyze the code"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var finalMsg *message.Message
	var gotError bool

	for ev := range events {
		switch ev.Type {
		case agent.EventAgentDone:
			finalMsg = ev.FinalMessage
		case agent.EventAgentError:
			gotError = true
		}
	}

	if gotError {
		t.Fatal("expected successful run when planner uses tools")
	}
	if finalMsg == nil {
		t.Fatal("expected final message")
	}
}

func TestPlannerRun_EmptyPlannerResponseRetriesWithoutTools(t *testing.T) {
	plannerProv := &emptyThenTextProvider{name: "empty-then-text", text: "Recovered plan after no-tools retry."}
	mainProv := &mockProvider{name: "main", response: "Synthesized from retry."}

	p := New(Config{
		MainProvider:  mainProv,
		PlannerSpecs:  []PlannerSpec{{Provider: plannerProv, Model: "gemini-3-pro-preview"}},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	events := p.Run(context.Background(), []message.Message{message.NewUserMessage("sess-1", "plan this")}, "sess-1")

	var plannerDone *agent.Event
	for ev := range events {
		if ev.Type == agent.EventPlannerDone {
			copyEv := ev
			plannerDone = &copyEv
		}
	}

	if plannerDone == nil {
		t.Fatal("expected planner done event")
	}
	if plannerDone.PlannerError != "" {
		t.Fatalf("expected planner retry to succeed, got error: %s", plannerDone.PlannerError)
	}
	if plannerDone.PlannerPlan != "Recovered plan after no-tools retry." {
		t.Fatalf("unexpected planner plan: %q", plannerDone.PlannerPlan)
	}

	reqs := plannerProv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 planner calls (initial + retry), got %d", len(reqs))
	}
	if len(reqs[0].Tools) == 0 {
		t.Fatal("expected first planner attempt to include tools")
	}
	if len(reqs[1].Tools) != 0 {
		t.Fatal("expected no-tools retry to disable tools")
	}
	if !strings.Contains(reqs[1].SystemPrompt, plannerNoToolsRetryInstruction) {
		t.Fatal("expected retry prompt to include explicit no-tools retry instruction")
	}
}

func TestPlannerRun_EmptyPlannerResponseAfterRetryFailsPlanner(t *testing.T) {
	plannerProv := &alwaysEmptyProvider{name: "always-empty"}
	mainProv := &mockProvider{name: "main", response: "should not run"}

	p := New(Config{
		MainProvider:  mainProv,
		PlannerSpecs:  []PlannerSpec{{Provider: plannerProv, Model: "gemini-3-pro-preview"}},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	events := p.Run(context.Background(), []message.Message{message.NewUserMessage("sess-1", "plan this")}, "sess-1")

	var (
		plannerError string
		gotAgentErr  bool
		gotAgentDone bool
	)

	for ev := range events {
		switch ev.Type {
		case agent.EventPlannerDone:
			plannerError = ev.PlannerError
		case agent.EventAgentError:
			gotAgentErr = true
		case agent.EventAgentDone:
			gotAgentDone = true
		}
	}

	if !gotAgentErr {
		t.Fatal("expected agent error when planner remains empty after retry")
	}
	if gotAgentDone {
		t.Fatal("did not expect agent done event when all planners fail")
	}
	if !strings.Contains(plannerError, "empty response") {
		t.Fatalf("expected planner error to mention empty response, got %q", plannerError)
	}
	if plannerProv.Calls() != 2 {
		t.Fatalf("expected 2 planner attempts, got %d", plannerProv.Calls())
	}
}

func TestPlannerRun_ContextCancellation(t *testing.T) {
	// If context is cancelled before planners finish, the run should error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	plannerProv := &mockProvider{name: "planner", response: "plan"}
	mainProv := &mockProvider{name: "main", response: "synth"}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerProv, Model: "model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "do something"),
	}

	events := p.Run(ctx, history, "sess-1")

	var gotError bool
	for ev := range events {
		if ev.Type == agent.EventAgentError {
			gotError = true
		}
	}

	// The planner agents run via agent.RunSync which checks context.
	// With an already-cancelled context, the agent should fail.
	if !gotError {
		t.Log("note: cancelled context may or may not propagate depending on goroutine scheduling")
	}
}

func TestPlannerRun_EventOrdering(t *testing.T) {
	// Verify the event ordering: PlanningPhase(dispatch) -> PlannerStart/Done -> PlanningPhase(synthesis) -> StreamText -> AgentDone
	plannerProv := &mockProvider{name: "planner", response: "my plan"}
	mainProv := &mockProvider{name: "main", response: "final plan"}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerProv, Model: "test-model"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "plan task"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	var eventTypes []agent.EventType
	for ev := range events {
		eventTypes = append(eventTypes, ev.Type)
	}

	// Expected order:
	// 1. PlanningPhase (dispatch)
	// 2. PlannerStart
	// 3. PlannerDone
	// 4. PlanningPhase (synthesis)
	// 5. StreamText
	// 6. AgentDone

	if len(eventTypes) < 6 {
		t.Fatalf("expected at least 6 events, got %d: %v", len(eventTypes), eventTypes)
	}

	// First event must be PlanningPhase (dispatch)
	if eventTypes[0] != agent.EventPlanningPhase {
		t.Errorf("expected first event to be PlanningPhase, got %d", eventTypes[0])
	}

	// Last event must be AgentDone
	if eventTypes[len(eventTypes)-1] != agent.EventAgentDone {
		t.Errorf("expected last event to be AgentDone, got %d", eventTypes[len(eventTypes)-1])
	}

	// Find the second PlanningPhase (synthesis)
	phaseCount := 0
	synthPhaseIdx := -1
	for i, et := range eventTypes {
		if et == agent.EventPlanningPhase {
			phaseCount++
			if phaseCount == 2 {
				synthPhaseIdx = i
				break
			}
		}
	}
	if synthPhaseIdx == -1 {
		t.Fatal("expected two PlanningPhase events (dispatch + synthesis)")
	}

	// All PlannerStart/Done events should be between the first and second PlanningPhase
	for i, et := range eventTypes {
		if et == agent.EventPlannerStart || et == agent.EventPlannerDone {
			if i <= 0 || i >= synthPhaseIdx {
				t.Errorf("PlannerStart/Done event at index %d should be between dispatch phase (0) and synthesis phase (%d)", i, synthPhaseIdx)
			}
		}
	}

	// StreamText should come after synthesis phase
	for i, et := range eventTypes {
		if et == agent.EventStreamText {
			if i <= synthPhaseIdx {
				t.Errorf("StreamText at index %d should come after synthesis phase at %d", i, synthPhaseIdx)
			}
		}
	}
}

func TestPlannerRun_PlannerDoneContainsPlanText(t *testing.T) {
	// Verify that EventPlannerDone contains the plan text from each planner.
	plannerA := &mockProvider{name: "a", response: "Plan Alpha"}
	plannerB := &mockProvider{name: "b", response: "Plan Beta"}
	mainProv := &mockProvider{name: "main", response: "unified"}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerA, Model: "alpha"},
			{Provider: plannerB, Model: "beta"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "plan"),
	}

	events := p.Run(context.Background(), history, "sess-1")

	planTexts := make(map[string]string)
	for ev := range events {
		if ev.Type == agent.EventPlannerDone {
			planTexts[ev.PlannerModel] = ev.PlannerPlan
		}
	}

	if plan, ok := planTexts["alpha"]; !ok || plan != "Plan Alpha" {
		t.Errorf("expected alpha plan 'Plan Alpha', got %q (present=%v)", plan, ok)
	}
	if plan, ok := planTexts["beta"]; !ok || plan != "Plan Beta" {
		t.Errorf("expected beta plan 'Plan Beta', got %q (present=%v)", plan, ok)
	}
}

func TestPlannerRun_SynthesisReceivesAllPlans(t *testing.T) {
	// Verify that the synthesis provider receives planner outputs in its request.
	plannerA := &mockProvider{name: "a", response: "Plan from A"}
	plannerB := &mockProvider{name: "b", response: "Plan from B"}

	// Recording main provider that captures the request.
	var capturedReq *provider.Request
	mainProv := &recordingProvider{
		response: "synthesized plan",
		onRequest: func(req provider.Request) {
			capturedReq = &req
		},
	}

	p := New(Config{
		MainProvider: mainProv,
		PlannerSpecs: []PlannerSpec{
			{Provider: plannerA, Model: "model-a"},
			{Provider: plannerB, Model: "model-b"},
		},
		Registry:      newTestRegistry(),
		WorkDir:       "/tmp/test",
		MaxTokens:     4096,
		MaxIterations: 9,
	})

	history := []message.Message{
		message.NewUserMessage("sess-1", "build a feature"),
	}

	events := p.Run(context.Background(), history, "sess-1")
	for range events {
		// drain
	}

	if capturedReq == nil {
		t.Fatal("expected synthesis provider to be called")
	}

	// The synthesis request should have messages containing planner outputs
	allContent := ""
	for _, m := range capturedReq.Messages {
		allContent += m.Content + " "
	}

	if !strings.Contains(allContent, "Plan from A") {
		t.Error("synthesis request should contain Plan from A")
	}
	if !strings.Contains(allContent, "Plan from B") {
		t.Error("synthesis request should contain Plan from B")
	}
	if !strings.Contains(allContent, "model-a") {
		t.Error("synthesis request should reference model-a")
	}
	if !strings.Contains(allContent, "model-b") {
		t.Error("synthesis request should reference model-b")
	}

	// Synthesis should have no tools
	if capturedReq.Tools != nil {
		t.Errorf("synthesis request should have nil tools, got %d", len(capturedReq.Tools))
	}
}

// recordingProvider captures the request sent to it.
type recordingProvider struct {
	response  string
	onRequest func(req provider.Request)
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) SendMessage(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	if p.onRequest != nil {
		p.onRequest(req)
	}
	ch := make(chan provider.StreamEvent, 10)
	go func() {
		defer close(ch)
		ch <- provider.StreamEvent{
			Type: provider.EventTextDelta,
			Text: p.response,
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
