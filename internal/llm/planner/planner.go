package planner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nullism/goder/internal/llm/agent"
	"github.com/nullism/goder/internal/llm/prompt"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/message"
	"github.com/nullism/goder/internal/tools"
)

// ReviewerSpec specifies a provider+model pair for the review agent.
type ReviewerSpec struct {
	Provider provider.Provider
	Model    string
}

const (
	reviewRoundTimeout  = 5 * time.Minute
	defaultReviewRounds = 3
	verdictApprove      = "APPROVE"
	verdictRevise       = "REVISE"
)

// Planner coordinates the reviewed planning flow:
// main draft -> reviewer critique -> main revision (iterative rounds)
// followed by a final summary for user approval.
type Planner struct {
	mainProvider  provider.Provider
	reviewerSpec  *ReviewerSpec
	registry      *tools.Registry
	workDir       string
	mainModel     string
	maxTokens     int
	maxIterations int
	reviewRounds  int
}

// Config holds planner construction parameters.
type Config struct {
	MainProvider  provider.Provider
	ReviewerSpec  *ReviewerSpec
	Registry      *tools.Registry
	WorkDir       string
	MainModel     string
	MaxTokens     int
	MaxIterations int
	ReviewRounds  int
}

// New creates a new Planner.
func New(cfg Config) *Planner {
	maxIter := cfg.MaxIterations
	if maxIter <= 0 {
		maxIter = agent.DefaultMaxIterations
	}
	rounds := cfg.ReviewRounds
	if rounds <= 0 {
		rounds = defaultReviewRounds
	}

	return &Planner{
		mainProvider:  cfg.MainProvider,
		reviewerSpec:  cfg.ReviewerSpec,
		registry:      cfg.Registry,
		workDir:       cfg.WorkDir,
		mainModel:     cfg.MainModel,
		maxTokens:     cfg.MaxTokens,
		maxIterations: maxIter,
		reviewRounds:  rounds,
	}
}

// Run executes the reviewed planning flow and returns a channel of agent.Event.
func (p *Planner) Run(ctx context.Context, history []message.Message, sessionID string) <-chan agent.Event {
	events := make(chan agent.Event, 64)

	go func() {
		defer close(events)
		p.run(ctx, history, sessionID, events)
	}()

	return events
}

func (p *Planner) run(ctx context.Context, history []message.Message, sessionID string, events chan<- agent.Event) {
	if p.reviewerSpec == nil || p.reviewerSpec.Provider == nil || p.reviewerSpec.Model == "" {
		events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("review agent is not configured")}
		return
	}

	events <- agent.Event{
		Type:      agent.EventPlanningPhase,
		PlanPhase: fmt.Sprintf("Starting plan-review loop (main=%s, reviewer=%s, rounds=%d)...", p.mainModel, p.reviewerSpec.Model, p.reviewRounds),
	}

	contextHistory := filterHistoryForContext(history)

	var currentDraft string
	var reviewerFeedback string
	var reviewerVerdict string
	agreed := false

	for round := 1; round <= p.reviewRounds; round++ {
		events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: fmt.Sprintf("Round %d/%d: drafting plan...", round, p.reviewRounds)}
		events <- agent.Event{Type: agent.EventPlannerStart, PlannerModel: "main:" + p.mainModel}

		draft, draftTokens, err := p.generateDraft(ctx, contextHistory, sessionID, round, currentDraft, reviewerFeedback)
		if err != nil {
			events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("main planning round %d failed: %w", round, err)}
			return
		}
		currentDraft = draft
		events <- agent.Event{
			Type:          agent.EventPlannerDone,
			PlannerModel:  "main:" + p.mainModel,
			PlannerPlan:   draft,
			PlannerTokens: draftTokens,
		}

		events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: fmt.Sprintf("Round %d/%d: reviewing draft...", round, p.reviewRounds)}
		events <- agent.Event{Type: agent.EventPlannerStart, PlannerModel: "reviewer:" + p.reviewerSpec.Model}

		reviewText, verdict, reviewTokens, err := p.reviewDraft(ctx, contextHistory, sessionID, round, draft)
		if err != nil {
			events <- agent.Event{
				Type:          agent.EventPlannerDone,
				PlannerModel:  "reviewer:" + p.reviewerSpec.Model,
				PlannerError:  err.Error(),
				PlannerTokens: reviewTokens,
			}
			events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("review round %d failed: %w", round, err)}
			return
		}

		reviewerFeedback = reviewText
		reviewerVerdict = verdict
		events <- agent.Event{
			Type:          agent.EventPlannerDone,
			PlannerModel:  "reviewer:" + p.reviewerSpec.Model,
			PlannerPlan:   reviewText,
			PlannerTokens: reviewTokens,
		}

		if verdict == verdictApprove {
			agreed = true
			events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: fmt.Sprintf("Reviewer approved the plan in round %d.", round)}
			break
		}
	}

	if !agreed {
		events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: "Review rounds exhausted without full agreement; summarizing best draft with open concerns..."}
	}

	events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: "Preparing final plan summary for user approval..."}

	summaryText, usage, err := p.summarizePlan(ctx, history, currentDraft, reviewerVerdict, reviewerFeedback, agreed, sessionID, events)
	if err != nil {
		events <- agent.Event{Type: agent.EventAgentError, Error: fmt.Errorf("final plan summary failed: %w", err)}
		return
	}

	finalMsg := message.NewAssistantMessage(sessionID, summaryText, nil)
	finalMsg.Model = p.mainModel
	finalMsg.InputTokens = usage.InputTokens
	finalMsg.OutputTokens = usage.OutputTokens
	finalMsg.TotalTokens = usage.TotalTokens
	events <- agent.Event{Type: agent.EventAgentDone, FinalMessage: &finalMsg}
}

func (p *Planner) generateDraft(ctx context.Context, contextHistory []message.Message, sessionID string, round int, previousDraft, reviewerFeedback string) (string, int, error) {
	planCtx, cancel := context.WithTimeout(ctx, reviewRoundTimeout)
	defer cancel()

	ag := agent.New(agent.Config{
		Provider:      p.mainProvider,
		Registry:      p.registry.ReadOnly(),
		PermSvc:       nil,
		WorkDir:       p.workDir,
		Model:         p.mainModel,
		MaxTokens:     p.maxTokens,
		MaxIterations: p.childIterationBudget(),
		SystemPrompt:  prompt.BuildPlanDraftPrompt(p.workDir, p.registry),
	})

	history := append([]message.Message{}, contextHistory...)
	history = append(history, message.NewUserMessage(sessionID, buildDraftInstruction(round, previousDraft, reviewerFeedback)))

	text, tokens, err := ag.RunSyncWithHistory(planCtx, history, sessionID)
	if err != nil {
		return "", tokens, err
	}
	if strings.TrimSpace(text) == "" {
		return "", tokens, fmt.Errorf("main agent returned an empty plan")
	}

	return text, tokens, nil
}

func (p *Planner) reviewDraft(ctx context.Context, contextHistory []message.Message, sessionID string, round int, draft string) (string, string, int, error) {
	reviewCtx, cancel := context.WithTimeout(ctx, reviewRoundTimeout)
	defer cancel()

	ag := agent.New(agent.Config{
		Provider:      p.reviewerSpec.Provider,
		Registry:      p.registry.ReadOnly(),
		PermSvc:       nil,
		WorkDir:       p.workDir,
		Model:         p.reviewerSpec.Model,
		MaxTokens:     p.maxTokens,
		MaxIterations: p.childIterationBudget(),
		SystemPrompt:  prompt.BuildPlanReviewPrompt(p.workDir, p.registry),
	})

	history := append([]message.Message{}, contextHistory...)
	history = append(history, message.NewUserMessage(sessionID, buildReviewInstruction(round, draft)))

	text, tokens, err := ag.RunSyncWithHistory(reviewCtx, history, sessionID)
	if err != nil {
		return "", "", tokens, err
	}
	if strings.TrimSpace(text) == "" {
		return "", "", tokens, fmt.Errorf("review agent returned an empty review")
	}

	return text, parseVerdict(text), tokens, nil
}

func (p *Planner) summarizePlan(ctx context.Context, history []message.Message, draft, verdict, reviewerFeedback string, agreed bool, sessionID string, events chan<- agent.Event) (string, provider.Usage, error) {
	summaryHistory := filterHistoryForContext(history)

	status := "not approved"
	if agreed {
		status = "approved"
	}

	summaryInput := "Create the final plan message for the user based on the reviewed plan loop.\n\n" +
		"Review status: " + status + "\n" +
		"Reviewer verdict: " + verdict + "\n\n" +
		"Latest main-agent draft:\n\n" + draft + "\n\n" +
		"Latest reviewer feedback:\n\n" + reviewerFeedback

	summaryHistory = append(summaryHistory, message.NewUserMessage(sessionID, summaryInput))

	req := provider.Request{
		SystemPrompt: prompt.BuildPlanSummaryPrompt(p.workDir),
		Messages:     summaryHistory,
		Tools:        nil,
		MaxTokens:    p.maxTokens,
	}

	streamCh, err := p.mainProvider.SendMessage(ctx, req)
	if err != nil {
		return "", provider.Usage{}, err
	}

	var text strings.Builder
	var usage provider.Usage

	for event := range streamCh {
		switch event.Type {
		case provider.EventTextDelta:
			text.WriteString(event.Text)
			events <- agent.Event{Type: agent.EventStreamText, Text: event.Text}
		case provider.EventError:
			return text.String(), usage, event.Error
		case provider.EventDone:
			usage = event.Usage
		}
	}

	if strings.TrimSpace(text.String()) == "" {
		return draft, usage, nil
	}

	return text.String(), usage, nil
}

func (p *Planner) childIterationBudget() int {
	den := p.reviewRounds * 2
	if den <= 0 {
		den = 2
	}
	budget := p.maxIterations / den
	if budget < 2 {
		budget = 2
	}
	return budget
}

func buildDraftInstruction(round int, previousDraft, reviewerFeedback string) string {
	if round <= 1 {
		return "Create an actionable implementation plan for the latest user request. Follow your required output format and include concrete proposed file changes."
	}

	return "Revise the implementation plan based on reviewer feedback and return a complete replacement plan.\n\n" +
		"Previous plan:\n\n" + previousDraft + "\n\n" +
		"Reviewer feedback:\n\n" + reviewerFeedback
}

func buildReviewInstruction(round int, draft string) string {
	return fmt.Sprintf("Review round %d. Evaluate the draft plan below for intent alignment, maintainability, and obvious security risks. Use the exact verdict contract.\n\nDraft plan:\n\n%s", round, draft)
}

func parseVerdict(review string) string {
	upper := strings.ToUpper(review)
	lines := strings.Split(upper, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "VERDICT:") {
			continue
		}
		verdict := strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
		if strings.HasPrefix(verdict, verdictApprove) {
			return verdictApprove
		}
		if strings.HasPrefix(verdict, verdictRevise) {
			return verdictRevise
		}
	}

	if strings.Contains(upper, "VERDICT: APPROVE") {
		return verdictApprove
	}

	return verdictRevise
}

// filterHistoryForContext creates a lightweight version of the conversation
// history suitable for giving child agents conversational context.
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
			if msg.Content != "" {
				filtered = append(filtered, message.Message{
					ID:        msg.ID,
					SessionID: msg.SessionID,
					Role:      msg.Role,
					Content:   msg.Content,
					CreatedAt: msg.CreatedAt,
				})
			}
		}
	}
	return filtered
}
