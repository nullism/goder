# Agents Documentation

## Overview

Goder is a terminal-based (TUI) AI coding assistant. It supports two execution modes: a **single-agent loop** (the default) and a **Main Agent + Planning Agents** model where multiple planning agents independently explore the codebase and produce plans, the main agent synthesizes them into a unified plan, and executes the plan itself.

## Single-Agent Mode

When no planning agents are configured, goder operates as a standard single-agent loop.

### Agent

The agent is implemented in `internal/llm/agent/agent.go`. It orchestrates the interaction between the user, the LLM provider, and the tool registry.

### Lifecycle

1. The user submits a message via the TUI.
2. The agent builds a system prompt and sends the full conversation history to the LLM provider.
3. The LLM streams back text and/or tool calls.
4. If tool calls are present, the agent executes them (with permission checks for destructive operations) and loops back to step 2 with the results appended.
5. The loop terminates when the LLM responds with no tool calls, or after `maxIterations` (default 25).

## Planning Mode

When planning agents are configured (via `agents.planners` in config or `GODER_PLANNING_AGENTS` env var), goder uses the Main Agent + Planning Agents model.

### Configuration

In `.goder.json` or `~/.config/goder/config.json`:

```json
{
  "agents": {
    "main": {
      "provider": "copilot",
      "model": "claude-sonnet-4.5"
    },
    "planners": [
      {"provider": "copilot", "model": "grok-code-fast-1"},
      {"provider": "openai",  "model": "gpt-4o"},
      {"provider": "copilot", "model": "claude-sonnet-4.5"}
    ]
  }
}
```

Or via environment variables:

```bash
export GODER_MAIN_PROVIDER=copilot
export GODER_MAIN_MODEL=claude-sonnet-4.5
export GODER_PLANNING_AGENTS="copilot:grok-code-fast-1,openai:gpt-4o,copilot:claude-sonnet-4.5"
```

If `agents.main` is omitted, the main agent falls back to the top-level `provider`/`model` settings.

### Planning Lifecycle

1. **Dispatch** — All planning agents receive the full user request concurrently. Each planner runs with a read-only tool registry (via `Registry.ReadOnly()`) containing only non-destructive tools (`glob`, `grep`, `view`, `ls`, `fetch`). Planners run with `maxIterations/3` budget.
   - If a planner returns an empty text response, goder retries that planner once with tools disabled and an explicit instruction to return a plain-text plan. If the retry is still empty, that planner is marked as failed.

2. **Synthesis** — The main agent's provider receives the original user request plus all planner outputs, and synthesizes them into a single coherent, unified plan. This plan is streamed to the TUI as regular text.

3. **Presentation** — The synthesized plan is presented to the user as a regular assistant message (no special modal or dialog).

4. **User Response** — The user types their next message naturally (e.g., "yes", "go ahead", "do it but skip step 3"). The main agent interprets the response and decides whether to execute, modify, or abandon the plan.

5. **Execution** — The main agent executes the plan itself using all available tools (`bash`, `write`, `edit`) with the permission system gating destructive operations.

### Key Design Decisions

- **All planners get the full task** — Unlike a subtask decomposition model, every planner receives the same complete user request. This produces diverse independent perspectives.
- **Planning agents are read-only** — Planners receive a filtered registry via `Registry.ReadOnly()` and `PermSvc: nil`, so they cannot modify files or run commands.
- **Main agent executes** — The main agent itself executes the synthesized plan; there is no separate execution agent.
- **Natural language approval** — The plan is a regular assistant message, and the user's response is interpreted by the main agent via the "Planning Agent Integration" section in its system prompt.

### Key Files

| File | Purpose |
|------|---------|
| `internal/llm/planner/planner.go` | Planner struct and 2-phase run flow (dispatch + synthesize) |
| `internal/llm/prompt/prompts/planner.md` | System prompt for planning agents |
| `internal/llm/prompt/prompts/synthesizer.md` | System prompt for the synthesis phase |
| `internal/llm/prompt/prompts/default.md` | Main agent prompt (includes "Planning Agent Integration" section) |

## Event System

The agent communicates with the TUI via typed events sent over a channel:

- `StreamText` — incremental text tokens from the LLM
- `ToolCallStart` / `ToolCallEnd` — tool invocation lifecycle
- `ToolResult` — output from a tool execution
- `AgentDone` — the agent loop has completed
- `AgentError` — an error occurred during the loop
- `PersistMessage` — signals the TUI/session to persist a message
- `PlanningPhase` — phase transition in the planning flow
- `PlannerStart` / `PlannerDone` — planning agent lifecycle
  - `PlannerDone` carries either planner plan text or planner error text for per-agent status display

## Tools

Tools are registered via a plugin-style `Registry` in `internal/tools/tool.go`. Each tool implements the `Tool` interface (`Name`, `Description`, `Parameters`, `RequiresPermission`, `Execute`).

### Built-in Tools

| Tool    | File                       | Permission | Description                              |
|---------|----------------------------|------------|------------------------------------------|
| `glob`  | `internal/tools/glob.go`   | No         | File pattern matching                    |
| `grep`  | `internal/tools/grep.go`   | No         | Regex content search                     |
| `view`  | `internal/tools/view.go`   | No         | Read files with line numbers and offset  |
| `ls`    | `internal/tools/ls.go`     | No         | Directory listing                        |
| `fetch` | `internal/tools/fetch.go`  | No         | HTTP GET for URLs                        |
| `bash`  | `internal/tools/bash.go`   | Yes        | Shell command execution                  |
| `write` | `internal/tools/write.go`  | Yes        | Create or overwrite files                |
| `edit`  | `internal/tools/edit.go`   | Yes        | Find-and-replace editing                 |

### Adding a New Tool

1. Create a new file in `internal/tools/` (e.g., `mytool.go`).
2. Implement the `Tool` interface:
   - `Name()` — unique tool identifier
   - `Description()` — human-readable description for the LLM
   - `Parameters()` — JSON Schema defining the tool's input
   - `RequiresPermission()` — return `true` if the tool is destructive
   - `Execute(ctx, args)` — perform the action and return a string result
3. Register the tool in the `Registry` (see `cmd/goder/main.go` for the wiring).

## Permission System

Destructive tools go through the permission service (`internal/permission/permission.go`). When the agent wants to execute a permissioned tool, the TUI displays an approval dialog with options to allow, deny, or allow for the remainder of the session.

Planning agents do not have access to the permission system and receive a read-only tool registry (via `Registry.ReadOnly()`), so they never even see destructive tools.

## LLM Provider

The LLM provider is abstracted behind the `Provider` interface in `internal/llm/provider/provider.go`. The current implementation (`openai.go`) uses the OpenAI Responses API with SSE streaming. Adding a new provider means implementing `SendMessage`, `ListModels`, `SetAPIKey`, and `SetModel`.

## Contributing

When modifying agent behavior, tools, or the permission system, please update this document to reflect the changes.
