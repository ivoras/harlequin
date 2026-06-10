// Package config loads and validates the Harlequin server configuration from a
// YAML file plus a .env file. Secrets live in environment variables and override
// any structural values from YAML.
package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ivoras/harlequin/internal/server/email"
	"github.com/ivoras/harlequin/internal/server/secrets"
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
	Prices          map[string]Price `yaml:"prices"`
	ContextWindows  map[string]int   `yaml:"context_windows"` // model id -> max input tokens
	Embeddings EmbeddingsConfig `yaml:"embeddings"`
	Agent      AgentConfig      `yaml:"agent"`
	Memory     MemoryConfig     `yaml:"memory"`
	Sessions   SessionsConfig   `yaml:"sessions"`
	MCP        MCPConfig        `yaml:"mcp"`
	Auth       AuthConfig       `yaml:"auth"`
	Email      email.Config     `yaml:"email"`

	// Secrets, populated from the environment (not YAML).
	JWTSecret string `yaml:"-"`
	DBPath    string `yaml:"-"`
	// SecretKey is the 32-byte AES master key for encrypting credentials at
	// rest (MCP header secrets and OAuth tokens), decoded from the base64
	// HARLEQUIN_SECRET_KEY env var. Nil when unset; features needing it fail closed.
	SecretKey []byte `yaml:"-"`
}

// AuthConfig controls authentication-related policy.
type AuthConfig struct {
	// AllowRegistration enables the public self-registration endpoints
	// (/auth/register + /auth/verify). Pointer so an omitted key defaults to
	// enabled; set false to require owner-created accounts only.
	AllowRegistration *bool `yaml:"allow_registration"`
}

// AllowRegistrationValue reports whether self-registration is enabled (default true).
func (a AuthConfig) AllowRegistrationValue() bool {
	return a.AllowRegistration == nil || *a.AllowRegistration
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Addr string `yaml:"addr"`
	// Web optionally serves the static browser SPA at / (same origin as the API).
	Web WebUIConfig `yaml:"web"`
}

// WebUIConfig controls serving the static web UI from the server.
type WebUIConfig struct {
	// Dir is the filesystem path to the built SPA (e.g. "./web/dist"). Empty
	// disables serving (e.g. when nginx serves the static files instead).
	Dir string `yaml:"dir"`
}

// ProviderConfig describes one chat LLM provider.
type ProviderConfig struct {
	Name          string `yaml:"name"`
	BaseURL       string `yaml:"base_url"`
	Model         string `yaml:"model"`
	APIKeyEnv     string `yaml:"api_key_env"`
	ContextWindow int    `yaml:"context_window"` // max input tokens for this provider's model
	// ReturnProgress asks the provider for live prompt-processing progress
	// (llama.cpp's `return_progress`: streamed `prompt_progress` events before the
	// first token). Only enable for llama.cpp servers; other backends may reject
	// the unknown request field.
	ReturnProgress bool `yaml:"return_progress"`

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
	// JSFetchAllowlist restricts the legacy GET-only fetch() to these hosts. When
	// WebFetch is enabled the sandbox fetch() instead routes through the web
	// fetcher (any public host, SSRF-guarded) and this list is ignored.
	JSFetchAllowlist []string `yaml:"js_fetch_allowlist"`
	// WebFetch enables/configures the WebFetch tool (fetch a URL, convert to
	// Markdown, analyse with a small model).
	WebFetch WebFetchConfig `yaml:"web_fetch"`
	// ReportTiming shows per-turn model operation timing (PP/TG/clock) in chat.
	ReportTiming bool `yaml:"report_timing"`
	// AutoTitle controls the background task that titles idle, generically-named
	// sessions using the default LLM.
	AutoTitle AutoTitleConfig `yaml:"auto_title"`
}

// AutoTitleConfig controls the background session auto-titler.
type AutoTitleConfig struct {
	// Enabled turns the auto-titler on (default true when omitted).
	Enabled *bool `yaml:"enabled"`
}

// EnabledValue reports whether the auto-titler runs (default true).
func (a AutoTitleConfig) EnabledValue() bool {
	return a.Enabled == nil || *a.Enabled
}

// WebFetchConfig controls the WebFetch tool.
type WebFetchConfig struct {
	// Enabled exposes the WebFetch tool to the model. Pointer so an omitted
	// config key defaults to enabled (see EnabledValue); set false to disable.
	Enabled *bool `yaml:"enabled"`
	// Model is the small, fast model used to analyse fetched content. Empty uses
	// the provider's default model.
	Model string `yaml:"model"`
	// Temperature for the content-analysis call. Low for consistent extraction;
	// pointer so an omitted key defaults via TemperatureValue (0.1).
	Temperature *float64 `yaml:"temperature"`
	// AllowPrivate permits fetching loopback/private/link-local addresses. Off by
	// default as an SSRF guard.
	AllowPrivate bool `yaml:"allow_private"`
}

// EnabledValue reports whether the WebFetch tool is exposed (default true).
func (w WebFetchConfig) EnabledValue() bool {
	return w.Enabled == nil || *w.Enabled
}

// TemperatureValue returns the content-analysis sampling temperature (default 0.1).
func (w WebFetchConfig) TemperatureValue() float64 {
	if w.Temperature != nil {
		return *w.Temperature
	}
	return 0.1
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
	// SlotSearchWeight is the RRF weight of the slot-key leg in memory search
	// (0 disables it). Default 1.0. See docs/memory_experiment_key_slots.md.
	SlotSearchWeight *float64 `yaml:"slot_search_weight"`
	// ExtractFromDocuments runs memory extraction over the text of an imported
	// document (in addition to indexing it for RAG), so uploads distill durable
	// facts. Pointer so an omitted key defaults to enabled; set false to make
	// document import RAG-only.
	ExtractFromDocuments *bool `yaml:"extract_from_documents"`
	// SearchMaxDistance drops vector/slot search candidates whose cosine distance
	// to the query exceeds this, so an unrelated query returns few/no results
	// instead of padding to the limit. Range [0,2]; default 0.2. 0 disables the
	// cutoff (the FTS leg is unaffected — it only matches real token hits).
	SearchMaxDistance *float64 `yaml:"search_max_distance"`
}

// ConflictCheckEnabled reports whether post-write conflict detection runs.
func (m MemoryConfig) ConflictCheckEnabled() bool {
	if m.ConflictCheck != nil {
		return *m.ConflictCheck
	}
	return true
}

// SlotSearchWeightValue returns the slot-key search-leg weight (default 1.0).
func (m MemoryConfig) SlotSearchWeightValue() float64 {
	if m.SlotSearchWeight != nil {
		return *m.SlotSearchWeight
	}
	return 1.0
}

// ExtractFromDocumentsEnabled reports whether imported documents also feed memory
// extraction (default true).
func (m MemoryConfig) ExtractFromDocumentsEnabled() bool {
	return m.ExtractFromDocuments == nil || *m.ExtractFromDocuments
}

// SearchMaxDistanceValue returns the cosine-distance cutoff for vector/slot
// search candidates (default 0.2; 0 disables it).
func (m MemoryConfig) SearchMaxDistanceValue() float64 {
	if m.SearchMaxDistance != nil {
		return *m.SearchMaxDistance
	}
	return 0.2
}

// SessionsConfig controls JSONL trajectory (session) logging.
type SessionsConfig struct {
	// Enabled turns trajectory JSONL logging on (default true when omitted).
	Enabled       *bool    `yaml:"enabled"`
	Dir           string   `yaml:"dir"`
	LogTokens     bool     `yaml:"log_tokens"`
	// RetentionDays deletes trajectory JSONL files older than this many days.
	// Unset defaults to 7; explicit 0 keeps files forever.
	RetentionDays *int     `yaml:"retention_days"`
	Redact        []string `yaml:"redact"`
}

// EnabledValue reports whether trajectory logs are written (default true).
func (s SessionsConfig) EnabledValue() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// RetentionDaysValue returns how long to keep session logs (default 7; 0 = forever).
func (s SessionsConfig) RetentionDaysValue() int {
	if s.RetentionDays == nil {
		return 7
	}
	return *s.RetentionDays
}

// MCPConfig controls the MCP (Model Context Protocol) client: whether external
// MCP servers may be registered and used as tool sources, and connection tuning.
type MCPConfig struct {
	// Enabled turns the MCP client on. Default false.
	Enabled bool `yaml:"enabled"`
	// AllowUserServers lets ordinary users register their own (user-scope) MCP
	// servers. Shared-scope servers are always admin-only. Default true.
	AllowUserServers *bool `yaml:"allow_user_servers"`
	// SessionIdle closes an idle pooled MCP session after this duration (default 5m).
	SessionIdle Duration `yaml:"session_idle"`
	// ToolsCacheTTL caches a server's tools/list for this long (default 5m).
	ToolsCacheTTL Duration `yaml:"tools_cache_ttl"`
	// OAuthCallbackBaseURL is the externally reachable base URL of this server,
	// used to build the OAuth redirect URI (e.g. "https://harlequin.example.com").
	// Required for OAuth-authenticated MCP servers.
	OAuthCallbackBaseURL string `yaml:"oauth_callback_base_url"`
}

// AllowUserServersValue reports whether users may register user-scope servers (default true).
func (m MCPConfig) AllowUserServersValue() bool {
	if m.AllowUserServers == nil {
		return true
	}
	return *m.AllowUserServers
}

// SessionIdleValue returns the idle session timeout (default 5m).
func (m MCPConfig) SessionIdleValue() time.Duration {
	if m.SessionIdle.D() <= 0 {
		return 5 * time.Minute
	}
	return m.SessionIdle.D()
}

// ToolsCacheTTLValue returns the tools/list cache TTL (default 5m).
func (m MCPConfig) ToolsCacheTTLValue() time.Duration {
	if m.ToolsCacheTTL.D() <= 0 {
		return 5 * time.Minute
	}
	return m.ToolsCacheTTL.D()
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

	if s := os.Getenv("HARLEQUIN_SECRET_KEY"); s != "" {
		if key, err := secrets.DecodeKey(s); err == nil {
			c.SecretKey = key
		} else {
			log.Printf("config: ignoring invalid HARLEQUIN_SECRET_KEY: %v", err)
		}
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

	if env := c.Email.PasswordEnv; env != "" {
		c.Email.Password = os.Getenv(env)
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
