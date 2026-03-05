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
	verdictSkipped      = "SKIPPED"
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
	reviewSkipped := false
	requiresReview := true

	for round := 1; round <= p.reviewRounds; round++ {
		events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: fmt.Sprintf("Round %d/%d: drafting plan...", round, p.reviewRounds)}
		events <- agent.Event{Type: agent.EventPlannerStart, PlannerModel: "main:" + p.mainModel}

		draft, draftTokens, err := p.generateDraft(ctx, contextHistory, sessionID, round, currentDraft, reviewerFeedback, events)
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

		if round == 1 {
			requiresReview = isSemiComplexPlan(draft)
			if !requiresReview {
				reviewSkipped = true
				agreed = true
				reviewerVerdict = verdictSkipped
				reviewerFeedback = "Reviewer skipped: plan classified as simple (short, low-risk scope)."
				events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: "Plan classified as simple; skipping reviewer and preparing final summary."}
				break
			}
		}

		events <- agent.Event{Type: agent.EventPlanningPhase, PlanPhase: fmt.Sprintf("Round %d/%d: reviewing draft...", round, p.reviewRounds)}
		events <- agent.Event{Type: agent.EventPlannerStart, PlannerModel: "reviewer:" + p.reviewerSpec.Model}

		reviewText, verdict, reviewTokens, err := p.reviewDraft(ctx, contextHistory, sessionID, round, draft, events)
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

	summaryText, usage, err := p.summarizePlan(ctx, history, currentDraft, reviewerVerdict, reviewerFeedback, agreed, reviewSkipped, sessionID, events)
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

func (p *Planner) generateDraft(ctx context.Context, contextHistory []message.Message, sessionID string, round int, previousDraft, reviewerFeedback string, events chan<- agent.Event) (string, int, error) {
	planCtx, cancel := context.WithTimeout(ctx, reviewRoundTimeout)
	defer cancel()

	baseInstruction := buildDraftInstruction(round, previousDraft, reviewerFeedback)
	first, err := p.runPlannerAgent(planCtx, plannerRunConfig{
		Provider:     p.mainProvider,
		Model:        p.mainModel,
		Registry:     p.registry.ReadOnly(),
		SystemPrompt: prompt.BuildPlanDraftPrompt(p.workDir, p.registry),
		History:      contextHistory,
		SessionID:    sessionID,
		Instruction:  baseInstruction,
		ToolPrefix:   "main",
		EventSink:    events,
	})
	if err != nil {
		return "", first.Tokens, err
	}

	if strings.TrimSpace(first.Text) == "" {
		return "", first.Tokens, fmt.Errorf("main agent returned an empty plan")
	}
	if first.RepoInspectionCalls > 0 {
		return first.Text, first.Tokens, nil
	}

	second, err := p.runPlannerAgent(planCtx, plannerRunConfig{
		Provider:     p.mainProvider,
		Model:        p.mainModel,
		Registry:     p.registry.ReadOnly(),
		SystemPrompt: prompt.BuildPlanDraftPrompt(p.workDir, p.registry),
		History:      contextHistory,
		SessionID:    sessionID,
		Instruction:  buildDraftInspectionRetryInstruction(round, previousDraft, reviewerFeedback),
		ToolPrefix:   "main",
		EventSink:    events,
	})
	totalTokens := first.Tokens + second.Tokens
	if err != nil {
		return "", totalTokens, err
	}
	if strings.TrimSpace(second.Text) == "" {
		return "", totalTokens, fmt.Errorf("main agent returned an empty plan after inspection retry")
	}
	if second.RepoInspectionCalls == 0 {
		return "", totalTokens, fmt.Errorf("main planning round %d did not inspect the repository with read-only tools", round)
	}

	return second.Text, totalTokens, nil
}

func (p *Planner) reviewDraft(ctx context.Context, contextHistory []message.Message, sessionID string, round int, draft string, events chan<- agent.Event) (string, string, int, error) {
	reviewCtx, cancel := context.WithTimeout(ctx, reviewRoundTimeout)
	defer cancel()

	first, err := p.runReviewAttempt(
		reviewCtx,
		contextHistory,
		sessionID,
		buildReviewInstruction(round, draft),
		p.registry.ReadOnly(),
		events,
	)
	if err != nil {
		return "", "", first.Tokens, err
	}

	if strings.TrimSpace(first.Text) != "" && first.RepoInspectionCalls > 0 {
		return first.Text, parseVerdict(first.Text), first.Tokens, nil
	}

	reason := "empty response"
	if strings.TrimSpace(first.Text) != "" && first.RepoInspectionCalls == 0 {
		reason = "missing repository inspection"
	}

	second, retryErr := p.runReviewAttempt(
		reviewCtx,
		contextHistory,
		sessionID,
		buildReviewRetryInstruction(round, draft, reason),
		p.registry.ReadOnly(),
		events,
	)
	totalTokens := first.Tokens + second.Tokens
	if retryErr != nil {
		return "", "", totalTokens, retryErr
	}
	if strings.TrimSpace(second.Text) != "" && second.RepoInspectionCalls > 0 {
		return second.Text, parseVerdict(second.Text), totalTokens, nil
	}

	fallback := syntheticInsufficientReview(round, reason)
	return fallback, verdictRevise, totalTokens, nil
}

func (p *Planner) runReviewAttempt(ctx context.Context, contextHistory []message.Message, sessionID, instruction string, registry *tools.Registry, events chan<- agent.Event) (plannerRunResult, error) {
	return p.runPlannerAgent(ctx, plannerRunConfig{
		Provider:     p.reviewerSpec.Provider,
		Model:        p.reviewerSpec.Model,
		Registry:     registry,
		SystemPrompt: prompt.BuildPlanReviewPrompt(p.workDir, p.registry),
		History:      contextHistory,
		SessionID:    sessionID,
		Instruction:  instruction,
		ToolPrefix:   "reviewer",
		EventSink:    events,
	})
}

func (p *Planner) summarizePlan(ctx context.Context, history []message.Message, draft, verdict, reviewerFeedback string, agreed, reviewSkipped bool, sessionID string, events chan<- agent.Event) (string, provider.Usage, error) {
	summaryHistory := filterHistoryForContext(history)

	status := "not approved"
	if reviewSkipped {
		status = "skipped for simple plan"
	} else if agreed {
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
		return "Create an actionable implementation plan for the latest user request. Inspect the repository with read-only tools before finalizing your plan. Follow your required output format and include concrete proposed file changes."
	}

	return "Revise the implementation plan based on reviewer feedback and return a complete replacement plan. Inspect the repository with read-only tools again to validate the revisions.\n\n" +
		"Previous plan:\n\n" + previousDraft + "\n\n" +
		"Reviewer feedback:\n\n" + reviewerFeedback
}

func buildDraftInspectionRetryInstruction(round int, previousDraft, reviewerFeedback string) string {
	base := buildDraftInstruction(round, previousDraft, reviewerFeedback)
	return base + "\n\nIMPORTANT: Your previous response did not use repository-inspection tool calls. You must call read-only repository tools (glob, grep, ls, or view) before responding."
}

func buildReviewInstruction(round int, draft string) string {
	return fmt.Sprintf("Review round %d. Evaluate the draft plan below for intent alignment, maintainability, and obvious security risks. Inspect repository files with read-only tools before finalizing your verdict. Use the exact verdict contract.\n\nDraft plan:\n\n%s", round, draft)
}

func buildReviewRetryInstruction(round int, draft, reason string) string {
	return fmt.Sprintf("Retry review round %d. Your previous response had an issue (%s). You must inspect the repository with read-only tools in this retry and then return plain text only with the exact verdict contract beginning with VERDICT: APPROVE or VERDICT: REVISE.\n\nDraft plan:\n\n%s", round, reason, draft)
}

func syntheticInsufficientReview(round int, reason string) string {
	return fmt.Sprintf("VERDICT: REVISE\n\nAssessment\nReviewer validation was insufficient in round %d (%s).\n\nFindings\n- Review output was empty or lacked repository-backed verification.\n\nRequired Revisions\n- Re-run review with repository inspection via read-only tools before approval.", round, reason)
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

type plannerRunConfig struct {
	Provider     provider.Provider
	Model        string
	Registry     *tools.Registry
	SystemPrompt string
	History      []message.Message
	SessionID    string
	Instruction  string
	ToolPrefix   string
	EventSink    chan<- agent.Event
}

type plannerRunResult struct {
	Text                string
	Tokens              int
	ToolCalls           int
	RepoInspectionCalls int
}

func (p *Planner) runPlannerAgent(ctx context.Context, cfg plannerRunConfig) (plannerRunResult, error) {
	ag := agent.New(agent.Config{
		Provider:      cfg.Provider,
		Registry:      cfg.Registry,
		PermSvc:       nil,
		WorkDir:       p.workDir,
		Model:         cfg.Model,
		MaxTokens:     p.maxTokens,
		MaxIterations: p.childIterationBudget(),
		SystemPrompt:  cfg.SystemPrompt,
	})

	history := append([]message.Message{}, cfg.History...)
	history = append(history, message.NewUserMessage(cfg.SessionID, cfg.Instruction))

	eventCh := ag.Run(ctx, history, cfg.SessionID)

	var out plannerRunResult
	var streamed strings.Builder

	for ev := range eventCh {
		switch ev.Type {
		case agent.EventStreamText:
			streamed.WriteString(ev.Text)
		case agent.EventToolCallStart:
			out.ToolCalls++
			if isRepoInspectionTool(ev.ToolCallName) {
				out.RepoInspectionCalls++
			}
		case agent.EventPersistMessage:
			if ev.FinalMessage != nil {
				out.Tokens += ev.FinalMessage.TotalTokens
			}
		case agent.EventAgentDone:
			if ev.FinalMessage != nil {
				out.Tokens += ev.FinalMessage.TotalTokens
				out.Text = ev.FinalMessage.Content
			} else {
				out.Text = streamed.String()
			}
			return out, nil
		case agent.EventAgentError:
			return out, ev.Error
		case agent.EventToolCallEnd:
			if cfg.ToolPrefix != "" && cfg.EventSink != nil {
				prefixed := ev
				prefixed.ToolCallID = prefixedToolID(cfg.ToolPrefix, ev.ToolCallID)
				prefixed.ToolCallName = prefixedToolName(cfg.ToolPrefix, ev.ToolCallName)
				cfg.EventSink <- prefixed
			}
		case agent.EventToolResult:
			if cfg.ToolPrefix != "" && cfg.EventSink != nil {
				prefixed := ev
				prefixed.ToolCallID = prefixedToolID(cfg.ToolPrefix, ev.ToolCallID)
				prefixed.ToolCallName = prefixedToolName(cfg.ToolPrefix, ev.ToolCallName)
				cfg.EventSink <- prefixed
			}
		}

		if ev.Type == agent.EventToolCallStart && cfg.ToolPrefix != "" && cfg.EventSink != nil {
			prefixed := ev
			prefixed.ToolCallID = prefixedToolID(cfg.ToolPrefix, ev.ToolCallID)
			prefixed.ToolCallName = prefixedToolName(cfg.ToolPrefix, ev.ToolCallName)
			cfg.EventSink <- prefixed
		}
	}

	out.Text = streamed.String()
	return out, nil
}

func isRepoInspectionTool(name string) bool {
	switch name {
	case "glob", "grep", "ls", "view":
		return true
	default:
		return false
	}
}

func prefixedToolName(prefix, name string) string {
	if prefix == "" {
		return name
	}
	if name == "" {
		return prefix
	}
	return prefix + ":" + name
}

func prefixedToolID(prefix, id string) string {
	if prefix == "" {
		return id
	}
	if id == "" {
		return prefix
	}
	return prefix + ":" + id
}

func isSemiComplexPlan(draft string) bool {
	nonHeadingLines := 0
	for _, raw := range strings.Split(draft, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") {
			continue
		}
		nonHeadingLines++
		if nonHeadingLines > 2 {
			return true
		}
	}

	return false
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
