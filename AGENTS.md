# Agents Documentation

## Overview

Goder is a terminal-based (TUI) AI coding assistant. At its core is an **agentic loop** that sends conversation context to an LLM, executes any requested tool calls, and feeds results back until the LLM produces a final response. This document describes the agent architecture and its components.

## Agent

The single agent is implemented in `internal/llm/agent/agent.go`. It orchestrates the interaction between the user, the LLM provider, and the tool registry.

### Lifecycle

1. The user submits a message via the TUI.
2. The agent builds a system prompt (mode-aware) and sends the full conversation history to the LLM provider.
3. The LLM streams back text and/or tool calls.
4. If tool calls are present, the agent executes them (with permission checks for destructive operations) and loops back to step 2 with the results appended.
5. The loop terminates when the LLM responds with no tool calls, or after `maxIterations` (default 25).

### Operating Modes

- **PLAN mode** (default): Read-only. The agent can explore the codebase using `glob`, `grep`, `view`, `ls`, and `fetch`, but cannot modify files or run commands.
- **BUILD mode**: Full capability. The agent can additionally use `bash`, `write`, and `edit`, with user permission required for destructive operations.

### Event System

The agent communicates with the TUI via typed events sent over a channel:

- `StreamText` — incremental text tokens from the LLM
- `ToolCallStart` / `ToolCallEnd` — tool invocation lifecycle
- `ToolResult` — output from a tool execution
- `AgentDone` — the agent loop has completed
- `AgentError` — an error occurred during the loop
- `PersistMessage` — signals the TUI/session to persist a message

## Tools

Tools are registered via a plugin-style `Registry` in `internal/tools/tool.go`. Each tool implements the `Tool` interface (`Name`, `Description`, `Parameters`, `RequiresPermission`, `Execute`).

### Built-in Tools

| Tool    | File                    | Mode  | Description                              |
|---------|-------------------------|-------|------------------------------------------|
| `glob`  | `internal/tools/glob.go`  | PLAN  | File pattern matching                    |
| `grep`  | `internal/tools/grep.go`  | PLAN  | Regex content search                     |
| `view`  | `internal/tools/view.go`  | PLAN  | Read files with line numbers and offset  |
| `ls`    | `internal/tools/ls.go`    | PLAN  | Directory listing                        |
| `fetch` | `internal/tools/fetch.go` | PLAN  | HTTP GET for URLs                        |
| `bash`  | `internal/tools/bash.go`  | BUILD | Shell command execution (needs permission) |
| `write` | `internal/tools/write.go` | BUILD | Create or overwrite files (needs permission) |
| `edit`  | `internal/tools/edit.go`  | BUILD | Find-and-replace editing (needs permission) |

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

Destructive tools (BUILD mode) go through the permission service (`internal/permission/permission.go`). When the agent wants to execute a permissioned tool, the TUI displays an approval dialog with options to allow, deny, or allow for the remainder of the session.

## LLM Provider

The LLM provider is abstracted behind the `Provider` interface in `internal/llm/provider/provider.go`. The current implementation (`openai.go`) uses the OpenAI Responses API with SSE streaming. Adding a new provider means implementing `SendMessage`, `ListModels`, `SetAPIKey`, and `SetModel`.

## Contributing

When modifying agent behavior, tools, or the permission system, please update this document to reflect the changes.
