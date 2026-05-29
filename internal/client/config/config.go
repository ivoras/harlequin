// Package config loads and persists the Harlequin client configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the client configuration.
type Config struct {
	ServerURL    string `yaml:"server_url"`
	SkillsDir    string `yaml:"skills_dir"`
	Theme        string `yaml:"theme"`
	ShowThinking bool   `yaml:"show_thinking"`
	Token        string `yaml:"token"`

	// path is where this config was loaded from / will be saved to.
	path string `yaml:"-"`
}

// DefaultPath returns the default client config path (~/.config/harlequin/client.yaml).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "client.yaml"
	}
	return filepath.Join(home, ".config", "harlequin", "client.yaml")
}

// Load reads the config at path (creating an empty one in memory if missing),
// applies defaults, and lets HARLEQUIN_TOKEN / HARLEQUIN_SERVER_URL override.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	cfg := &Config{path: path}

	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse client config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read client config: %w", err)
	}
	cfg.path = path

	cfg.applyDefaults()

	if v := os.Getenv("HARLEQUIN_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("HARLEQUIN_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("HARLEQUIN_SHOW_THINKING"); v != "" {
		cfg.ShowThinking = v == "1" || v == "true" || v == "yes"
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.ServerURL == "" {
		c.ServerURL = "http://127.0.0.1:8080"
	}
	if c.SkillsDir == "" {
		c.SkillsDir = "~/.agents/skills"
	}
	if c.Theme == "" {
		c.Theme = "dark-purple-green"
	}
}

// ExpandedSkillsDir returns SkillsDir with a leading ~ expanded to the home dir.
func (c *Config) ExpandedSkillsDir() string {
	return expandHome(c.SkillsDir)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// Save writes the config back to its path (creating parent dirs).
func (c *Config) Save() error {
	if c.path == "" {
		c.path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, raw, 0o600)
}

// Path returns the config file path.
func (c *Config) Path() string { return c.path }
