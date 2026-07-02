// Package skills parses, stores, and resolves skills and hats. The server is
// the single source of truth: both live in the scoped SQLite databases (skills
// resolve project → shared → user; hats are shared-only), baked-in copies are
// seeded from the binary at startup, and the resolver renders <?js ?> templates
// on read.
package skills

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the YAML header of a SKILL.md.
type Frontmatter struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	Tools       []ToolDefinition `yaml:"tools"`
}

// ToolDefinition is a skill-declared agent tool.
type ToolDefinition struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
	Run         string         `yaml:"run"`
}

// Skill is a resolved skill: its files plus parsed metadata.
type Skill struct {
	Name        string
	Description string
	Tools       []ToolDefinition
	// Files maps relative path -> contents (includes SKILL.md).
	Files map[string]string
	// Source is the scope the skill resolved from: "project", "shared", or
	// "user" — or "hat" when a worn hat's override supplied it.
	Source string
}

// SkillMarkdown returns the raw SKILL.md contents.
func (s *Skill) SkillMarkdown() string {
	return s.Files["SKILL.md"]
}

// parseFrontmatter splits a SKILL.md into its YAML frontmatter and body.
func parseFrontmatter(content string) (Frontmatter, string, error) {
	var fm Frontmatter
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return fm, content, fmt.Errorf("missing YAML frontmatter")
	}
	rest := trimmed[3:]
	// Find the closing delimiter at the start of a line.
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, content, fmt.Errorf("unterminated frontmatter")
	}
	header := rest[:idx]
	body := rest[idx+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	if i := strings.IndexByte(body, '\n'); strings.HasPrefix(body, "-") {
		// handle case where closing line had extra dashes/newline
		_ = i
	}
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return fm, body, fmt.Errorf("parse frontmatter: %w", err)
	}
	return fm, body, nil
}

// buildSkill constructs a Skill from its files and source.
func buildSkill(name string, files map[string]string, source string) (*Skill, error) {
	md, ok := files["SKILL.md"]
	if !ok {
		return nil, fmt.Errorf("skill %q has no SKILL.md", name)
	}
	fm, _, err := parseFrontmatter(md)
	if err != nil {
		return nil, fmt.Errorf("skill %q: %w", name, err)
	}
	if fm.Name == "" {
		fm.Name = name
	}
	return &Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Tools:       fm.Tools,
		Files:       files,
		Source:      source,
	}, nil
}
