[![Go Tests](https://github.com/nullism/goder/actions/workflows/test.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/test.yml)
[![Go Lint](https://github.com/nullism/goder/actions/workflows/lint.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/lint.yml)

# goder

goder is a terminal coding agent written in Go.

It runs in your current working directory, supports tool-driven code changes, and supports a **main + reviewer + programmer** architecture.

## Highlights

- Go implementation (no React render loop in terminal)
- Linear, transparent output with visible tool-call results
- Main orchestrator with optional reviewed planning loop and dedicated programmer agent
- Works directly against files in the current project directory

## Installation

### From source

1. Install Go 1.24+
2. Clone this repository
3. Build the binary:
   ```bash
   go build -o goder ./cmd/goder
   ```
4. Run it:
   ```bash
   ./goder
   ```
5. (Optional) Move it into your PATH:
   ```bash
   mv goder /usr/local/bin/
   ```

## Runtime startup behavior

At startup, goder:

1. Loads config (`defaults < config file < env vars`)
2. Opens/creates SQLite at `DBPath()` (under the configured data dir)
3. Initializes session, tools, and permission services
4. Initializes the main orchestrator provider/model (`agents.main` or top-level fallback)
5. Optionally initializes a reviewer provider/model if reviewer config is present
6. Initializes the programmer provider/model (`agents.programmer` or main fallback)
7. Starts the TUI

If reviewer initialization fails, goder warns and continues with reviewer disabled.

## Configuration

### Config file lookup order

goder reads the **first existing** file from:

1. `./.goder.json` (project-local)
2. `$XDG_CONFIG_HOME/goder/config.json` (via `os.UserConfigDir`)
3. `~/.goder.json` (legacy location)

Then environment variables are applied on top.

### Default values

- `provider`: `openai`
- `model`: `gpt-4o`
- `maxTokens`: `4096`
- `maxIterations`: `50`
- `reviewIterations`: `3`
- `shell`: `/bin/bash` (or `cmd.exe` on Windows)
- `debug`: `false`

`workDir` is always the process current directory (not serialized in config).

### Data directory and DB

Data directory resolution:

1. `GODER_DATA_DIR` (if set)
2. `$XDG_DATA_HOME/goder`
3. `~/.local/share/goder`

DB file path is `${dataDir}/goder.db`.

### Environment variables

General:

- `GODER_PROVIDER`
- `GODER_MODEL`
- `GODER_SHELL`
- `GODER_MAX_ITERATIONS`
- `GODER_REVIEW_ITERATIONS`
- `GODER_ALWAYS_REVIEW`
- `GODER_DATA_DIR`

Agent-specific overrides:

- `GODER_MAIN_PROVIDER`
- `GODER_MAIN_MODEL`
- `GODER_REVIEWER_PROVIDER`
- `GODER_REVIEWER_MODEL`
- `GODER_PROGRAMMER_PROVIDER`
- `GODER_PROGRAMMER_MODEL`

Legacy compatibility:

- `GODER_PLANNING_AGENTS` (`provider:model,provider:model,...`)
  - If `agents.reviewer` is not set, the first legacy planner is used as reviewer.

Provider API keys:

- `OPENAI_API_KEY` (for `openai`)
- `ANTHROPIC_API_KEY` (for `anthropic`)
- `GITHUB_TOKEN` (for `copilot`)

You can also set `providerKeys` in config for per-provider credentials.

### Example `.goder.json`

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
      "provider": "anthropic",
      "model": "claude-sonnet-4-20250514"
    },
    "programmer": {
      "provider": "openai",
      "model": "gpt-4.1"
    }
  }
}
```

## Agent behavior

- **Main (orchestrator)** always runs with read-only tools and decides whether to respond, run review loop, or call programmer.
- **Reviewer enabled** when `agents.reviewer` exists and reviewer model is non-empty.
- **Programmer** provider/model can be set independently via `agents.programmer`; falls back to main if omitted.
- Main and reviewer receive read-only tools only.
- Programmer receives full tools (write/exec still permission-gated).
- Programmer execution is allowed only after the user explicitly approves the reviewed plan.

## Built-in tools

Registered by default:

Read-oriented tools:
- `glob`
- `grep`
- `ls`
- `view`

Write/exec tools (permission-gated):
- `bash`
- `write`
- `edit`

Network tool:
- `fetch`

Planning/read-only contexts only receive non-permission tools.

## Notes

- goder operates relative to the current working directory.
- Project-local config (`.goder.json`) takes precedence over user-level config files.
