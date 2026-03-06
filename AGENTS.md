# Agent Architecture

Goder uses three logical roles:

1. **Main (orchestrator)**: decides workflow and can inspect with read-only tools.
2. **Reviewer**: critiques plans in the review loop with read-only tools.
3. **Programmer**: writes code and may use write/exec tools.

The main agent decides when to trigger a plan review loop.
After a reviewed plan is produced, programmer execution is allowed only after explicit user approval.

## Configuration

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "maxIterations": 50,
  "reviewIterations": 3,
  "alwaysReview": false,
  "agents": {
    "main": {
      "provider": "openai",
      "model": "gpt-4o"
    },
    "reviewer": {
      "provider": "copilot",
      "model": "claude-sonnet-4.5"
    },
    "programmer": {
      "provider": "openai",
      "model": "gpt-4.1"
    }
  }
}
```

Environment overrides:

- `GODER_MAIN_PROVIDER`, `GODER_MAIN_MODEL`
- `GODER_REVIEWER_PROVIDER`, `GODER_REVIEWER_MODEL`
- `GODER_PROGRAMMER_PROVIDER`, `GODER_PROGRAMMER_MODEL`
- `GODER_REVIEW_ITERATIONS`
- `GODER_ALWAYS_REVIEW`

Legacy `GODER_PLANNING_AGENTS` is still read for backward compatibility (first entry is used as reviewer if reviewer is not configured).

## Review Loop Behavior

1. Main orchestrator decides whether to respond directly, run review loop, or request programmer execution.
2. If review loop is triggered, main drafts and reviewer critiques iteratively.
3. If reviewer says `REVISE`, another round begins.
4. If reviewer says `APPROVE` (or rounds exhaust), goder summarizes the plan and asks the user whether to proceed.
5. Programmer execution is only allowed after explicit user approval.

## Safety Model

- Main and reviewer agents only receive `Registry.ReadOnly()` tools.
- Only programmer receives the full tool registry (including write/exec tools), subject to permission checks.

## Key Files

- `internal/llm/planner/planner.go`: reviewed planning orchestration.
- `internal/llm/prompt/prompts/plan_draft.md`: main planning prompt.
- `internal/llm/prompt/prompts/reviewer.md`: reviewer prompt and verdict contract.
- `internal/llm/prompt/prompts/plan_summary.md`: final user-facing plan summary prompt.
- `internal/config/config.go`: reviewer config and env parsing.
- `internal/tui/model.go`, `internal/tui/settings.go`: runtime wiring and settings UI.
