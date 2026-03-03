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

	// Initialize LLM provider
	prov, err := provider.New(cfg.Provider, cfg.APIKeyFor(cfg.Provider), cfg.Model)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// Build planning agent specs if configured
	var plannerSpecs []planner.PlannerSpec

	if cfg.PlanningEnabled() {
		for _, pa := range cfg.Agents.Planners {
			planProv, err := provider.New(pa.Provider, cfg.APIKeyFor(pa.Provider), pa.Model)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: skipping planning agent %s:%s: %v\n", pa.Provider, pa.Model, err)
				continue
			}
			plannerSpecs = append(plannerSpecs, planner.PlannerSpec{
				Provider: planProv,
				Model:    pa.Model,
			})
		}

		if len(plannerSpecs) == 0 {
			fmt.Fprintf(os.Stderr, "warning: no valid planning agents configured, planning disabled\n")
		}
	}

	// If a main agent is configured with a different provider/model, use that
	mainProvName := cfg.MainAgentProvider()
	mainModel := cfg.MainAgentModel()
	if mainProvName != cfg.Provider || mainModel != cfg.Model {
		prov, err = provider.New(mainProvName, cfg.APIKeyFor(mainProvName), mainModel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating main agent provider: %v\n", err)
			os.Exit(1)
		}
	}

	// Create the TUI model
	model := tui.New(cfg, database, sessionSvc, registry, prov, permSvc, plannerSpecs)

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
