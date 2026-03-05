# Agent Architecture

Goder supports two execution modes:

1. **Single-agent loop** (default): the main agent plans and implements directly.
2. **Main + Review loop**: the main agent drafts a plan, a review agent critiques it, and the cycle iterates for a configured number of rounds before a final summarized plan is shown to the user.

When no review agent is configured, goder runs in single-agent mode.

## Configuration

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "maxIterations": 50,
  "reviewIterations": 3,
  "agents": {
    "main": {
      "provider": "openai",
      "model": "gpt-4o"
    },
    "reviewer": {
      "provider": "copilot",
      "model": "claude-sonnet-4.5"
    }
  }
}
```

Environment overrides:

- `GODER_MAIN_PROVIDER`, `GODER_MAIN_MODEL`
- `GODER_REVIEWER_PROVIDER`, `GODER_REVIEWER_MODEL`
- `GODER_REVIEW_ITERATIONS`

Legacy `GODER_PLANNING_AGENTS` is still read for backward compatibility (first entry is used as reviewer if reviewer is not configured).

## Review Loop Behavior

1. Main agent creates a plan draft using read-only tools.
2. Review agent evaluates intent alignment, maintainability, and obvious security risks.
3. If reviewer says `REVISE`, main agent revises and another round begins.
4. If reviewer says `APPROVE`, goder summarizes the agreed plan with proposed file changes and asks the user whether to proceed.
5. If rounds are exhausted, goder still summarizes the best draft and includes open concerns.

## Safety Model

- During planning/review rounds, agents only receive `Registry.ReadOnly()` tools.
- File edits and command execution happen only after user approval in the normal main-agent execution loop.

## Key Files

- `internal/llm/planner/planner.go`: reviewed planning orchestration.
- `internal/llm/prompt/prompts/plan_draft.md`: main planning prompt.
- `internal/llm/prompt/prompts/reviewer.md`: reviewer prompt and verdict contract.
- `internal/llm/prompt/prompts/plan_summary.md`: final user-facing plan summary prompt.
- `internal/config/config.go`: reviewer config and env parsing.
- `internal/tui/model.go`, `internal/tui/settings.go`: runtime wiring and settings UI.
