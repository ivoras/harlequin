// Package config loads and validates the Harlequin server configuration from a
// YAML file plus a .env file. Secrets live in environment variables and override
// any structural values from YAML.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that unmarshals from YAML strings like "2s" (or
// an empty string, which means zero).
type Duration time.Duration

// UnmarshalYAML parses a duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	s := value.Value
	if s == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the full server configuration.
type Config struct {
	Server     ServerConfig     `yaml:"server"`
	DataDir    string           `yaml:"data_dir"`
	Providers  []ProviderConfig `yaml:"providers"`
	Routing    RoutingConfig    `yaml:"routing"`
	Prices     map[string]Price `yaml:"prices"`
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	Agent      AgentConfig      `yaml:"agent"`
	Memory     MemoryConfig     `yaml:"memory"`
	Sessions   SessionsConfig   `yaml:"sessions"`

	// Secrets, populated from the environment (not YAML).
	JWTSecret string `yaml:"-"`
	DBPath    string `yaml:"-"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Addr string `yaml:"addr"`
}

// ProviderConfig describes one chat LLM provider.
type ProviderConfig struct {
	Name      string `yaml:"name"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	APIKeyEnv string `yaml:"api_key_env"`

	// APIKey is resolved from APIKeyEnv at load time.
	APIKey string `yaml:"-"`
}

// RoutingConfig controls provider selection and fallback.
type RoutingConfig struct {
	DefaultProvider string            `yaml:"default_provider"`
	FallbackOrder   []string          `yaml:"fallback_order"`
	ModelRules      map[string]string `yaml:"model_rules"`
}

// Price is the per-1K-token cost for a model.
type Price struct {
	PromptPer1K     float64 `yaml:"prompt_per_1k"`
	CompletionPer1K float64 `yaml:"completion_per_1k"`
}

// EmbeddingsConfig is the dedicated embeddings provider.
type EmbeddingsConfig struct {
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	Dim       int    `yaml:"dim"`
	APIKeyEnv string `yaml:"api_key_env"`

	APIKey string `yaml:"-"`
}

// AgentConfig controls the agent loop and JS sandbox.
type AgentConfig struct {
	MaxSteps           int      `yaml:"max_steps"`
	Temperature        *float64 `yaml:"temperature"` // chat LLM sampling; default 0.2
	SkillRenderTimeout Duration `yaml:"skill_render_timeout"`
	JSToolTimeout      Duration `yaml:"js_tool_timeout"`
	JSOutputCap        int      `yaml:"js_output_cap"`
	JSFetchAllowlist   []string `yaml:"js_fetch_allowlist"`
}

// TemperatureValue returns the configured chat temperature (default 0.2).
func (a AgentConfig) TemperatureValue() float64 {
	if a.Temperature != nil {
		return *a.Temperature
	}
	return 0.2
}

// MemoryConfig controls memory behaviour.
type MemoryConfig struct {
	AutoExtract        bool     `yaml:"auto_extract"`
	DefaultTTL         Duration `yaml:"default_ttl"`
	ConflictCheck      *bool    `yaml:"conflict_check"` // default true when unset
	ConflictCandidates int      `yaml:"conflict_candidates"`
}

// ConflictCheckEnabled reports whether post-write conflict detection runs.
func (m MemoryConfig) ConflictCheckEnabled() bool {
	if m.ConflictCheck != nil {
		return *m.ConflictCheck
	}
	return true
}

// SessionsConfig controls JSONL session logging.
type SessionsConfig struct {
	Enabled       bool     `yaml:"enabled"`
	Dir           string   `yaml:"dir"`
	LogTokens     bool     `yaml:"log_tokens"`
	RetentionDays int      `yaml:"retention_days"`
	Redact        []string `yaml:"redact"`
}

// Load reads the YAML config at path, loads .env (if present), resolves secrets,
// applies defaults, and validates.
func Load(path string) (*Config, error) {
	// Load .env first so env vars are available. Ignore "not found".
	_ = godotenv.Load()

	cfg := &Config{}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}

	cfg.applyDefaults()
	cfg.resolveSecrets()

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Agent.MaxSteps == 0 {
		c.Agent.MaxSteps = 8
	}
	if c.Agent.SkillRenderTimeout == 0 {
		c.Agent.SkillRenderTimeout = Duration(2 * time.Second)
	}
	if c.Agent.JSToolTimeout == 0 {
		c.Agent.JSToolTimeout = Duration(10 * time.Second)
	}
	if c.Agent.JSOutputCap == 0 {
		c.Agent.JSOutputCap = 64 * 1024
	}
	if c.Embeddings.Dim == 0 {
		c.Embeddings.Dim = 1536
	}
	if c.Sessions.Dir == "" {
		c.Sessions.Dir = filepath.Join(c.DataDir, "sessions")
	}
	if c.Memory.ConflictCandidates == 0 {
		c.Memory.ConflictCandidates = 8
	}
}

func (c *Config) resolveSecrets() {
	c.JWTSecret = os.Getenv("JWT_SECRET")

	c.DBPath = os.Getenv("HARLEQUIN_DB_PATH")
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "harlequin.db")
	}

	for i := range c.Providers {
		if env := c.Providers[i].APIKeyEnv; env != "" {
			c.Providers[i].APIKey = os.Getenv(env)
		}
		// Fall back to the generic LLM_API_KEY if none specified.
		if c.Providers[i].APIKey == "" {
			c.Providers[i].APIKey = os.Getenv("LLM_API_KEY")
		}
	}

	if env := c.Embeddings.APIKeyEnv; env != "" {
		c.Embeddings.APIKey = os.Getenv(env)
	}
	if c.Embeddings.APIKey == "" {
		c.Embeddings.APIKey = os.Getenv("EMBED_API_KEY")
	}
}

func (c *Config) validate() error {
	if c.JWTSecret == "" {
		return fmt.Errorf("JWT_SECRET must be set (in .env or environment)")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}
	for _, p := range c.Providers {
		if p.Name == "" || p.BaseURL == "" || p.Model == "" {
			return fmt.Errorf("provider %q is missing name/base_url/model", p.Name)
		}
	}
	if c.Embeddings.BaseURL == "" || c.Embeddings.Model == "" {
		return fmt.Errorf("embeddings base_url and model must be set")
	}
	return nil
}

// SessionsDir returns the resolved sessions directory.
func (c *Config) SessionsDir() string {
	if c.Sessions.Dir != "" {
		return c.Sessions.Dir
	}
	return filepath.Join(c.DataDir, "sessions")
}

// SkillsDir returns the deployed skills directory.
func (c *Config) SkillsDir() string {
	return filepath.Join(c.DataDir, "skills")
}

// HatsDir returns the deployed hats directory.
func (c *Config) HatsDir() string {
	return filepath.Join(c.DataDir, "hats")
}

// ProviderByName returns the provider config with the given name, or nil.
func (c *Config) ProviderByName(name string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}
