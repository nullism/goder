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
const plannerTimeout = 2 * time.Minute

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

	synthText, err := p.synthesize(ctx, history, results, sessionID, events)
	if err != nil {
		events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("synthesis failed: %w", err)}
		return
	}

	// Present the synthesized plan as the final response.
	// The user can then approve, modify, or reject it in their next message.
	finalMsg := message.NewAssistantMessage(sessionID, synthText, nil)
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

	// Extract the last user message to use as the task prompt
	var taskPrompt string
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == message.User {
			taskPrompt = history[i].Content
			break
		}
	}

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

			planText, err := ag.RunSync(planCtx, taskPrompt, sessionID)
			results[idx] = plannerResult{
				Model: spec.Model,
				Plan:  planText,
				Err:   err,
			}

			if err != nil {
				events <- agent.Event{
					Type:         agent.EventPlannerDone,
					PlannerModel: spec.Model,
					PlannerPlan:  fmt.Sprintf("Error: %s", err.Error()),
				}
			} else {
				events <- agent.Event{
					Type:         agent.EventPlannerDone,
					PlannerModel: spec.Model,
					PlannerPlan:  planText,
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
func (p *Planner) synthesize(ctx context.Context, history []message.Message, results []plannerResult, sessionID string, events chan<- agent.Event) (string, error) {
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

	// Build synthesis history: original user message + planner outputs
	var synthHistory []message.Message
	// Include the last user message from the original history
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == message.User {
			synthHistory = append(synthHistory, history[i])
			break
		}
	}
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
		return "", fmt.Errorf("synthesis request failed: %w", err)
	}

	var finalText strings.Builder

	for event := range streamCh {
		switch event.Type {
		case provider.EventTextDelta:
			finalText.WriteString(event.Text)
			events <- agent.Event{Type: agent.EventStreamText, Text: event.Text}
		case provider.EventError:
			return finalText.String(), event.Error
		case provider.EventDone:
			// Usage tracking handled by the TUI when it persists the message
		}
	}

	return finalText.String(), nil
}
