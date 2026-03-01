![Go Tests](https://github.com/nullism/goder/actions/workflows/test.yml/badge.svg)

# goder
A simple Golang TUI for agentic coding.

## How to Run goder

1. **Ensure Go is Installed:** Make sure you have Go installed (version specified in `go.mod` is 1.25.7).

2. **Set Up Configuration:**
   - Ensure you have the necessary configuration files in place. Based on `main.go`, a configuration loader is used, suggesting a config file is needed.

3. **Build the Application:**
   - In the terminal, navigate to the project root and run:
     ```bash
     go build -o goder ./cmd/goder
     ```

4. **Run the Application:**
   - After building, run the application using:
     ```bash
     ./goder
     ```

5. **Additional Dependencies:**
   - Ensure any dependencies (like environment variables for API keys, especially if using OpenAI) are configured as per your setup needs.

6. **Running:**
   - Once built, running `./goder` will start the TUI application in your terminal.
