package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nullism/goder/internal/config"
	"github.com/nullism/goder/internal/db"
	"github.com/nullism/goder/internal/llm/planner"
	"github.com/nullism/goder/internal/llm/provider"
	"github.com/nullism/goder/internal/permission"
	"github.com/nullism/goder/internal/session"
	"github.com/nullism/goder/internal/tools"
	"github.com/nullism/goder/internal/tui"
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

	// Initialize main orchestrator provider/model.
	mainProvName := cfg.MainAgentProvider()
	mainModel := cfg.MainAgentModel()
	mainProv, err := provider.New(mainProvName, cfg.APIKeyFor(mainProvName), mainModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating main agent provider (%s:%s): %v\n", mainProvName, mainModel, err)
		os.Exit(1)
	}
	applyOpenAIOAuthMode(mainProvName, mainProv, cfg)

	// Build review agent spec if configured
	var reviewerSpec *planner.ReviewerSpec
	if cfg.ReviewEnabled() {
		reviewerProvider := cfg.ReviewerAgentProvider()
		reviewerModel := cfg.ReviewerAgentModel()
		reviewProv, err := provider.New(reviewerProvider, cfg.APIKeyFor(reviewerProvider), reviewerModel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reviewer disabled (%s:%s): %v\n", reviewerProvider, reviewerModel, err)
		} else {
			applyOpenAIOAuthMode(reviewerProvider, reviewProv, cfg)
			reviewerSpec = &planner.ReviewerSpec{Provider: reviewProv, Model: reviewerModel}
		}
	}

	// Initialize programmer provider/model.
	programmerProvider := cfg.ProgrammerAgentProvider()
	programmerModel := cfg.ProgrammerAgentModel()
	programmerProv, err := provider.New(programmerProvider, cfg.APIKeyFor(programmerProvider), programmerModel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating programmer agent provider (%s:%s): %v\n", programmerProvider, programmerModel, err)
		os.Exit(1)
	}
	applyOpenAIOAuthMode(programmerProvider, programmerProv, cfg)

	// Create the TUI model
	model := tui.New(cfg, database, sessionSvc, registry, mainProv, programmerProv, permSvc, reviewerSpec)

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

func applyOpenAIOAuthMode(providerName string, prov provider.Provider, cfg config.Config) {
	if providerName != "openai" {
		return
	}
	oai, ok := prov.(*provider.OpenAIProvider)
	if !ok {
		return
	}
	authCfg, ok := cfg.AuthFor("openai")
	if !ok || authCfg.Type != "oauth" {
		oai.SetOAuthCodexMode(false, "")
		return
	}
	oai.SetOAuthCodexMode(true, authCfg.AccountID)
}
