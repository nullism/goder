package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// AgentSpec specifies a provider+model pair for an agent.
type AgentSpec struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AgentsConfig holds the named agent configuration.
// The main agent drives the conversation and executes plans.
// Planning agents independently explore the codebase and produce plans.
type AgentsConfig struct {
	// Main is the provider+model for the main agent.
	// Falls back to the top-level Provider/Model if omitted.
	Main *AgentSpec `json:"main,omitempty"`

	// Planners is the pool of provider+model pairs for planning agents.
	// Each planner receives the full user request and independently
	// produces a plan. If empty, planning is disabled and the main
	// agent operates as a single agent.
	Planners []AgentSpec `json:"planners,omitempty"`
}

// Config holds the application configuration.
type Config struct {
	// Provider is the LLM provider name (e.g. "openai", "anthropic").
	Provider string `json:"provider"`

	// Model is the model identifier (e.g. "gpt-4o", "claude-sonnet-4-20250514").
	Model string `json:"model"`

	// APIKey is the provider API key. Loaded from environment if not set in config.
	// Deprecated: use ProviderKeys for per-provider credentials.
	APIKey string `json:"apiKey,omitempty"`

	// ProviderKeys stores API keys keyed by provider name.
	ProviderKeys map[string]string `json:"providerKeys,omitempty"`

	// MaxTokens is the maximum number of tokens in the LLM response.
	MaxTokens int `json:"maxTokens"`

	// DataDir is the directory for persistent storage (SQLite DB, etc.).
	DataDir string `json:"dataDir,omitempty"`

	// Shell is the shell to use for the bash tool.
	Shell string `json:"shell,omitempty"`

	// MaxIterations is the maximum number of agent loop iterations before stopping.
	MaxIterations int `json:"maxIterations"`

	// Debug enables debug logging.
	Debug bool `json:"debug"`

	// WorkDir is the working directory. Defaults to cwd.
	WorkDir string `json:"-"`

	// Agents holds the named agent configuration (main + planners).
	Agents AgentsConfig `json:"agents,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	shell := "/bin/bash"
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
	}

	return Config{
		Provider:      "openai",
		Model:         "gpt-4o",
		MaxTokens:     4096,
		MaxIterations: 50,
		Shell:         shell,
		Debug:         false,
	}
}

// Load reads configuration from files and environment variables.
// Priority: defaults < config file < environment variables.
func Load() (Config, error) {
	cfg := DefaultConfig()

	// Set working directory
	cwd, err := os.Getwd()
	if err != nil {
		return cfg, fmt.Errorf("getting working directory: %w", err)
	}
	cfg.WorkDir = cwd

	// Set default data directory
	cfg.DataDir, err = defaultDataDir()
	if err != nil {
		return cfg, fmt.Errorf("determining data directory: %w", err)
	}

	// Try to load config file (project-local first, then user-level)
	configPaths := []string{
		filepath.Join(cwd, ".goder.json"),
	}

	if configDir, err := os.UserConfigDir(); err == nil {
		configPaths = append(configPaths, filepath.Join(configDir, "goder", "config.json"))
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		configPaths = append(configPaths, filepath.Join(homeDir, ".goder.json"))
	}

	for _, path := range configPaths {
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parsing config %s: %w", path, err)
			}
			break
		}
	}

	// Environment variable overrides
	if v := os.Getenv("GODER_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("GODER_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("GODER_SHELL"); v != "" {
		cfg.Shell = v
	}
	if v := os.Getenv("GODER_MAX_ITERATIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxIterations = n
		}
	}
	if v := os.Getenv("GODER_MAIN_PROVIDER"); v != "" {
		if cfg.Agents.Main == nil {
			cfg.Agents.Main = &AgentSpec{}
		}
		cfg.Agents.Main.Provider = v
	}
	if v := os.Getenv("GODER_MAIN_MODEL"); v != "" {
		if cfg.Agents.Main == nil {
			cfg.Agents.Main = &AgentSpec{}
		}
		cfg.Agents.Main.Model = v
	}
	if v := os.Getenv("GODER_PLANNING_AGENTS"); v != "" {
		cfg.Agents.Planners = parsePlannerAgents(v)
	}

	// Load API key from provider-specific env var / legacy field.
	if cfg.ProviderKeys == nil {
		cfg.ProviderKeys = make(map[string]string)
	}
	if cfg.APIKey != "" && cfg.ProviderKeys[cfg.Provider] == "" {
		cfg.ProviderKeys[cfg.Provider] = cfg.APIKey
	}
	if cfg.ProviderKeys[cfg.Provider] == "" {
		cfg.ProviderKeys[cfg.Provider] = apiKeyFromEnv(cfg.Provider)
	}
	cfg.APIKey = cfg.ProviderKeys[cfg.Provider]

	// Ensure data directory exists
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return cfg, fmt.Errorf("creating data directory: %w", err)
	}

	return cfg, nil
}

// apiKeyFromEnv returns the API key for the given provider from environment variables.
func apiKeyFromEnv(provider string) string {
	switch provider {
	case "openai":
		return os.Getenv("OPENAI_API_KEY")
	case "anthropic":
		return os.Getenv("ANTHROPIC_API_KEY")
	case "copilot":
		return os.Getenv("GITHUB_TOKEN")
	default:
		return os.Getenv("OPENAI_API_KEY")
	}
}

// APIKeyFor returns the API key for the given provider.
func (c Config) APIKeyFor(provider string) string {
	if c.ProviderKeys != nil {
		if v := c.ProviderKeys[provider]; v != "" {
			return v
		}
	}
	if provider == c.Provider {
		return c.APIKey
	}
	return ""
}

// SetAPIKeyFor sets the API key for the given provider.
func (c *Config) SetAPIKeyFor(provider, key string) {
	if c.ProviderKeys == nil {
		c.ProviderKeys = make(map[string]string)
	}
	c.ProviderKeys[provider] = key
	if provider == c.Provider {
		c.APIKey = key
	}
}

// defaultDataDir returns the default data directory for persistent storage.
func defaultDataDir() (string, error) {
	if v := os.Getenv("GODER_DATA_DIR"); v != "" {
		return v, nil
	}

	// Use XDG_DATA_HOME if available, otherwise ~/.local/share
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "goder"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "goder"), nil
}

// Save persists the configuration to the user-level config file
// (~/.config/goder/config.json or $XDG_CONFIG_HOME/goder/config.json).
// Only serializable fields are written; WorkDir is excluded (json:"-").
func Save(cfg Config) error {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("determining config directory: %w", err)
	}

	dir := filepath.Join(configDir, "goder")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}

// DBPath returns the path to the SQLite database file.
func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "goder.db")
}

// PlanningEnabled returns true if planning agents are configured.
func (c Config) PlanningEnabled() bool {
	return len(c.Agents.Planners) > 0
}

// MainAgentProvider returns the provider for the main agent,
// falling back to the top-level Provider if not set.
func (c Config) MainAgentProvider() string {
	if c.Agents.Main != nil && c.Agents.Main.Provider != "" {
		return c.Agents.Main.Provider
	}
	return c.Provider
}

// MainAgentModel returns the model for the main agent,
// falling back to the top-level Model if not set.
func (c Config) MainAgentModel() string {
	if c.Agents.Main != nil && c.Agents.Main.Model != "" {
		return c.Agents.Main.Model
	}
	return c.Model
}

// parsePlannerAgents parses a comma-separated list of "provider:model" pairs.
// Example: "copilot:grok-code-fast-1,openai:gpt-4o,copilot:claude-sonnet-4.5"
func parsePlannerAgents(s string) []AgentSpec {
	var specs []AgentSpec
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		prov := strings.TrimSpace(parts[0])
		model := strings.TrimSpace(parts[1])
		if prov == "" || model == "" {
			continue
		}
		specs = append(specs, AgentSpec{Provider: prov, Model: model})
	}
	return specs
}
