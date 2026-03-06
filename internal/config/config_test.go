package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProgrammerAgentDefaultsToMain(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider = "openai"
	cfg.Model = "gpt-4o"
	cfg.Agents.Main = &AgentSpec{Provider: "copilot", Model: "claude-main"}

	if got := cfg.ProgrammerAgentProvider(); got != "copilot" {
		t.Fatalf("ProgrammerAgentProvider() = %q, want %q", got, "copilot")
	}
	if got := cfg.ProgrammerAgentModel(); got != "claude-main" {
		t.Fatalf("ProgrammerAgentModel() = %q, want %q", got, "claude-main")
	}
}

func TestProgrammerAgentOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agents.Main = &AgentSpec{Provider: "openai", Model: "gpt-main"}
	cfg.Agents.Programmer = &AgentSpec{Provider: "copilot", Model: "gpt-prog"}

	if got := cfg.ProgrammerAgentProvider(); got != "copilot" {
		t.Fatalf("ProgrammerAgentProvider() = %q, want %q", got, "copilot")
	}
	if got := cfg.ProgrammerAgentModel(); got != "gpt-prog" {
		t.Fatalf("ProgrammerAgentModel() = %q, want %q", got, "gpt-prog")
	}
}

func TestLoadProgrammerEnvOverrides(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWD)
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("GODER_PROGRAMMER_PROVIDER", "copilot")
	t.Setenv("GODER_PROGRAMMER_MODEL", "gpt-programmer")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Agents.Programmer == nil {
		t.Fatal("expected programmer agent to be set from env")
	}
	if got := cfg.Agents.Programmer.Provider; got != "copilot" {
		t.Fatalf("programmer provider = %q, want %q", got, "copilot")
	}
	if got := cfg.Agents.Programmer.Model; got != "gpt-programmer" {
		t.Fatalf("programmer model = %q, want %q", got, "gpt-programmer")
	}
}
