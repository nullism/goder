package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Config holds the application configuration.
type Config struct {
	// Provider is the LLM provider name (e.g. "openai", "anthropic").
	Provider string `json:"provider"`

	// Model is the model identifier (e.g. "gpt-4o", "claude-sonnet-4-20250514").
	Model string `json:"model"`

	// APIKey is the provider API key. Loaded from environment if not set in config.
	APIKey string `json:"apiKey,omitempty"`

	// MaxTokens is the maximum number of tokens in the LLM response.
	MaxTokens int `json:"maxTokens"`

	// DataDir is the directory for persistent storage (SQLite DB, etc.).
	DataDir string `json:"dataDir,omitempty"`

	// Shell is the shell to use for the bash tool.
	Shell string `json:"shell,omitempty"`

	// Debug enables debug logging.
	Debug bool `json:"debug"`

	// WorkDir is the working directory. Defaults to cwd.
	WorkDir string `json:"-"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	shell := "/bin/bash"
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
	}

	return Config{
		Provider:  "openai",
		Model:     "gpt-4o",
		MaxTokens: 4096,
		Shell:     shell,
		Debug:     false,
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

	// Load API key from provider-specific env var
	if cfg.APIKey == "" {
		cfg.APIKey = apiKeyFromEnv(cfg.Provider)
	}

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
	default:
		return os.Getenv("OPENAI_API_KEY")
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
