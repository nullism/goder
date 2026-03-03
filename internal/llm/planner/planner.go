package planner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nullism/goder/internal/llm/agent"
	"github.com/nullism/goder/internal/llm/prompt"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/permission"
	"github.com/nullism/goder/internal/tools"
)

// PlannerSpec specifies a provider+model pair for a planning agent.
type PlannerSpec struct {
	Provider provider.Provider
	Model    string
}

// plannerTimeout is the maximum time a single planning agent can run before
// being cancelled. This prevents one slow/hung planner from blocking the
// entire planning flow.
const plannerTimeout = 5 * time.Minute

// plannerResult holds the output from a single planning agent.
type plannerResult struct {
	Model string
	Plan  string
	Err   error
}

// Planner coordinates the planning flow: dispatch planning agents
// concurrently, collect their plans, then synthesize them into a
// unified plan via the main agent's provider.
type Planner struct {
	mainProvider  provider.Provider
	plannerSpecs  []PlannerSpec
	registry      *tools.Registry
	permSvc       *permission.Service
	workDir       string
	mainModel     string
	maxTokens     int
	maxIterations int
}

// Config holds planner construction parameters.
type Config struct {
	MainProvider  provider.Provider
	PlannerSpecs  []PlannerSpec
	Registry      *tools.Registry
	PermSvc       *permission.Service
	WorkDir       string
	MainModel     string
	MaxTokens     int
	MaxIterations int
}

// New creates a new Planner.
func New(cfg Config) *Planner {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = agent.DefaultMaxIterations
	}
	return &Planner{
		mainProvider:  cfg.MainProvider,
		plannerSpecs:  cfg.PlannerSpecs,
		registry:      cfg.Registry,
		permSvc:       cfg.PermSvc,
		workDir:       cfg.WorkDir,
		mainModel:     cfg.MainModel,
		maxTokens:     cfg.MaxTokens,
		maxIterations: maxIter,
	}
}

// Run executes the planning flow: dispatch planning agents → collect plans →
// synthesize into a unified plan. It returns a channel of agent.Event that
// the TUI can consume identically to a regular agent run.
func (p *Planner) Run(ctx context.Context, history []message.Message, sessionID string) <-chan agent.Event {
	events := make(chan agent.Event, 64)

	go func() {
		defer close(events)
		p.run(ctx, history, sessionID, events)
	}()

	return events
}

func (p *Planner) run(ctx context.Context, history []message.Message, sessionID string, events chan<- agent.Event) {
	// ── Phase 1: Dispatch Planning Agents ────────────────────────────
	modelList := make([]string, len(p.plannerSpecs))
	for i, ps := range p.plannerSpecs {
		modelList[i] = ps.Model
	}
	events <- agent.Event{
		Type:      agent.EventPlanningPhase,
		PlanPhase: fmt.Sprintf("Dispatching to %d planning agents [%s]...", len(p.plannerSpecs), strings.Join(modelList, ", ")),
	}

	results := p.dispatchPlanners(ctx, history, sessionID, events)

	// Check if all planners failed
	allFailed := true
	for _, r := range results {
		if r.Err == nil {
			allFailed = false
			break
		}
	}
	if allFailed {
		events <- agent.Event{
			Type:  agent.EventAgentError,
			Error: fmt.Errorf("all planning agents failed"),
		}
		return
	}

	// ── Phase 2: Synthesis ──────────────────────────────────────────
	events <- agent.Event{
		Type:      agent.EventPlanningPhase,
		PlanPhase: "Synthesizing plans into a unified response...",
	}

	synthText, synthUsage, err := p.synthesize(ctx, history, results, sessionID, events)
	if err != nil {
		events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("synthesis failed: %w", err)}
		return
	}

	// Present the synthesized plan as the final response.
	// The user can then approve, modify, or reject it in their next message.
	finalMsg := message.NewAssistantMessage(sessionID, synthText, nil)
	finalMsg.Model = p.mainModel
	finalMsg.InputTokens = synthUsage.InputTokens
	finalMsg.OutputTokens = synthUsage.OutputTokens
	finalMsg.TotalTokens = synthUsage.TotalTokens
	events <- agent.Event{Type: agent.EventAgentDone, FinalMessage: &finalMsg}
}

// dispatchPlanners runs all planning agents concurrently. Each planner
// receives the full user request and independently explores the codebase
// to produce a plan.
func (p *Planner) dispatchPlanners(ctx context.Context, history []message.Message, sessionID string, events chan<- agent.Event) []plannerResult {
	results := make([]plannerResult, len(p.plannerSpecs))

	var wg sync.WaitGroup
	// All planners run concurrently
	sem := make(chan struct{}, len(p.plannerSpecs))

	plannerPrompt := prompt.BuildPlannerPrompt(p.workDir, p.registry)

	// Build a filtered history with conversational context (user + assistant
	// text only, no tool calls/results) so planners understand references
	// to earlier parts of the conversation.
	contextHistory := filterHistoryForContext(history)

	for i, spec := range p.plannerSpecs {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore slot

		go func(idx int, spec PlannerSpec) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore slot

			// Per-planner timeout so one hung planner can't block the whole flow.
			planCtx, planCancel := context.WithTimeout(ctx, plannerTimeout)
			defer planCancel()

			events <- agent.Event{
				Type:         agent.EventPlannerStart,
				PlannerModel: spec.Model,
			}

			ag := agent.New(agent.Config{
				Provider:      spec.Provider,
				Registry:      p.registry.ReadOnly(), // planners only get read-only tools
				PermSvc:       nil,                   // planners don't get permission prompts
				WorkDir:       p.workDir,
				Model:         spec.Model,
				MaxTokens:     p.maxTokens,
				MaxIterations: p.maxIterations / 3, // budget per planner
				SystemPrompt:  plannerPrompt,
			})

			planText, planTokens, err := ag.RunSyncWithHistory(planCtx, contextHistory, sessionID)
			results[idx] = plannerResult{
				Model: spec.Model,
				Plan:  planText,
				Err:   err,
			}

			if err != nil {
				events <- agent.Event{
					Type:          agent.EventPlannerDone,
					PlannerModel:  spec.Model,
					PlannerPlan:   fmt.Sprintf("Error: %s", err.Error()),
					PlannerTokens: planTokens,
				}
			} else {
				events <- agent.Event{
					Type:          agent.EventPlannerDone,
					PlannerModel:  spec.Model,
					PlannerPlan:   planText,
					PlannerTokens: planTokens,
				}
			}
		}(i, spec)
	}

	wg.Wait()
	return results
}

// synthesize makes a final LLM call via the main agent's provider to
// combine all planning agent plans into a unified response, streaming
// the result to the TUI.
func (p *Planner) synthesize(ctx context.Context, history []message.Message, results []plannerResult, sessionID string, events chan<- agent.Event) (string, provider.Usage, error) {
	// Build the synthesis input with all planner outputs
	var plansText strings.Builder
	plansText.WriteString("# Planning Agent Plans\n\n")
	for i, r := range results {
		fmt.Fprintf(&plansText, "## Plan from Agent %d (%s)\n\n", i+1, r.Model)
		if r.Err != nil {
			fmt.Fprintf(&plansText, "**Status:** FAILED — %s\n\n", r.Err.Error())
		} else {
			fmt.Fprintf(&plansText, "%s\n\n", r.Plan)
		}
	}

	// Build synthesis history: filtered conversation context + planner outputs.
	// Include the full conversational context (user + assistant text) so the
	// synthesizer understands what was discussed earlier.
	synthHistory := filterHistoryForContext(history)
	// Add the planner outputs as a user message for synthesis
	synthHistory = append(synthHistory, message.NewUserMessage(sessionID,
		"Here are the plans from the planning agents. Synthesize them into a unified plan.\n\n"+plansText.String()))

	synthPrompt := prompt.BuildSynthesisPrompt(p.workDir)

	// Make the synthesis LLM call — no tools, just text synthesis
	req := provider.Request{
		SystemPrompt: synthPrompt,
		Messages:     synthHistory,
		Tools:        nil,
		MaxTokens:    p.maxTokens,
	}

	streamCh, err := p.mainProvider.SendMessage(ctx, req)
	if err != nil {
		return "", provider.Usage{}, fmt.Errorf("synthesis request failed: %w", err)
	}

	var finalText strings.Builder
	var usage provider.Usage

	for event := range streamCh {
		switch event.Type {
		case provider.EventTextDelta:
			finalText.WriteString(event.Text)
			events <- agent.Event{Type: agent.EventStreamText, Text: event.Text}
		case provider.EventError:
			return finalText.String(), usage, event.Error
		case provider.EventDone:
			usage = event.Usage
		}
	}

	return finalText.String(), usage, nil
}

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
