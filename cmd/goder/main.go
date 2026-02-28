package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/webgovernor/goder/internal/config"
	"github.com/webgovernor/goder/internal/db"
	"github.com/webgovernor/goder/internal/llm/provider"
	"github.com/webgovernor/goder/internal/permission"
	"github.com/webgovernor/goder/internal/session"
	"github.com/webgovernor/goder/internal/tools"
	"github.com/webgovernor/goder/internal/tui"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	// Initialize database
	database, err := db.New(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Initialize services
	sessionSvc := session.NewService(database)
	registry := tools.DefaultRegistry(cfg.WorkDir)
	permSvc := permission.NewService()

	// Initialize LLM provider
	var prov provider.Provider
	switch cfg.Provider {
	case "openai":
		prov = provider.NewOpenAIProvider(cfg.APIKey, cfg.Model)
	default:
		fmt.Fprintf(os.Stderr, "error: unsupported provider %q (supported: openai)\n", cfg.Provider)
		os.Exit(1)
	}

	// Create the TUI model
	model := tui.New(cfg, database, sessionSvc, registry, prov, permSvc)

	// Create the program
	p := tea.NewProgram(
		model,
	)

	// Give the model a reference to the program for async events
	model.SetProgram(p)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
