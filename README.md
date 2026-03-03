[![Go Tests](https://github.com/nullism/goder/actions/workflows/test.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/test.yml)
[![Go Lint](https://github.com/nullism/goder/actions/workflows/lint.yml/badge.svg)](https://github.com/nullism/goder/actions/workflows/lint.yml)

# goder

Goder is a fast agentic TUI in a similar class to OpenCode or Claude Code.

Goder is built with Goder (~70% at the time of this writing).

<img width="2701" height="1506" alt="image" src="https://github.com/user-attachments/assets/40f4460d-67fb-4609-8eea-d5b4d4a1621c" />


## Differences with other TUIs

1. Uses **multiple different planning agents** - like a panel of experts.
2. Written in Go instead of Javascript.
3. Does not use a render loop or React in the terminal.
4. Does not use mouse capture or alt screens.
   - Last output page is retained in terminal after exiting.
5. Very fast.
   - At the time of this writing, uses about 1/3 memory and 1/4th the CPU of others.
6. Linear and transparent output.
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


