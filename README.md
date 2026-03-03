[![Go Tests](https://github.com/nullism/goder/actions/workflows/test.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/test.yml)
[![Go Lint](https://github.com/nullism/goder/actions/workflows/lint.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/lint.yml)

# goder

Goder is a fast agentic TUI in a similar class to OpenCode or Claude Code.

Goder is built with Goder (~70% at the time of this writing).

<img width="1965" height="1214" alt="goder screenshot" src="https://github.com/user-attachments/assets/4f6bb677-de1a-4e8c-9e94-dcd6e2fe22e8" />

## Differences with other TUIs

1. Written in Go instead of Javascript.
2. Does not use a render loop or React in the terminal.
3. Does not use mouse capture or alt screens.
   - Last output page is retained in terminal after exiting.
4. Very fast.
   - At the time of this writing, uses about 1/3 memory and 1/4th the CPU of others.
5. Linear and transparent output.
  - Tool call output is visible.

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
5. (Optional) Move it into your PATH so it works from anywhere:
   ```bash
   mv goder /usr/local/bin/
   ```


