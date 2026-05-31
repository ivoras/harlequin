package skills

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ivoras/harlequin/internal/shared/types"
	"gopkg.in/yaml.v3"
)

// hatPromptFile is the (optional) system prompt + metadata file of a hat.
const hatPromptFile = "system_prompt.md"

// ErrHatNotFound is returned when a hat directory does not exist.
var ErrHatNotFound = errors.New("hat not found")

// hatFrontmatter is the optional YAML header of a hat's system_prompt.md. The
// skills list is the hat's visible-skill set (which skills are available while
// the hat is worn); an empty list means "all skills".
type hatFrontmatter struct {
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
}

func (m *Manager) hatDir(name string) string       { return filepath.Join(m.hatsDir, name) }
func (m *Manager) hatSkillsDir(name string) string { return filepath.Join(m.hatsDir, name, "skills") }

// ListHats returns the deployed hats.
func (m *Manager) ListHats() ([]types.Hat, error) {
	names, err := dirNames(m.hatsDir)
	if err != nil {
		return nil, err
	}
	var out []types.Hat
	for _, n := range names {
		h, err := m.GetHat(n)
		if err != nil {
			continue
		}
		out = append(out, *h)
	}
	return out, nil
}

// GetHat reads a hat from the filesystem: its optional system-prompt body and
// frontmatter (description + the skills it makes visible).
func (m *Manager) GetHat(name string) (*types.Hat, error) {
	if _, err := os.Stat(m.hatDir(name)); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrHatNotFound
		}
		return nil, err
	}
	h := &types.Hat{Name: name}
	if raw, err := m.cache.read(filepath.Join(m.hatDir(name), hatPromptFile)); err == nil {
		fm, body := splitHatFrontmatter(string(raw))
		h.Description = fm.Description
		h.Skills = fm.Skills
		h.SystemPrompt = strings.TrimSpace(body)
	}
	return h, nil
}

// splitHatFrontmatter separates an optional YAML frontmatter header from the
// template body (no error: a file without frontmatter is all body).
func splitHatFrontmatter(content string) (hatFrontmatter, string) {
	var fm hatFrontmatter
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return fm, content
	}
	rest := trimmed[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, content
	}
	header := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
	_ = yaml.Unmarshal([]byte(header), &fm)
	return fm, body
}

// resolveRawForHat resolves a skill's raw files while a hat is worn: a per-hat
// override (hats/<hat>/skills/<name>) wins; otherwise normal resolution applies.
func (m *Manager) resolveRawForHat(ctx context.Context, userDB *sql.DB, hat, name string) (map[string]string, string, error) {
	override := filepath.Join(m.hatSkillsDir(hat), name)
	if _, err := os.Stat(override); err == nil {
		files, err := m.readDir(override)
		if err != nil {
			return nil, "", err
		}
		return files, "hat", nil
	}
	return m.resolveRaw(ctx, userDB, name)
}

// ResolveEffective resolves a skill honoring the worn hat's overrides. hat may
// be nil (normal resolution).
func (m *Manager) ResolveEffective(ctx context.Context, userDB *sql.DB, name string, userID int64, username string, hat *types.Hat) (*Skill, error) {
	if hat == nil {
		return m.Resolve(ctx, userDB, name, userID, username)
	}
	files, source, err := m.resolveRawForHat(ctx, userDB, hat.Name, name)
	if err != nil {
		return nil, err
	}
	rendered, err := m.renderFiles(files, name, userID, username)
	if err != nil {
		return nil, err
	}
	return buildSkill(name, rendered, source)
}

// EffectiveSkillInfos returns the skills visible to a user given the worn hat:
//   - no hat → the normal global set;
//   - hat with a skills list → exactly those (its visibility list), with per-hat
//     overrides applied;
//   - hat without a list → the global set plus any hat overrides.
func (m *Manager) EffectiveSkillInfos(ctx context.Context, userDB *sql.DB, userID int64, username string, hat *types.Hat) ([]types.SkillInfo, error) {
	if hat == nil {
		return m.List(ctx, userDB, userID, username)
	}
	names := hat.Skills
	if len(names) == 0 {
		set := map[string]bool{}
		g, err := m.globalNames(ctx, userDB)
		if err != nil {
			return nil, err
		}
		for _, n := range g {
			set[n] = true
		}
		o, _ := dirNames(m.hatSkillsDir(hat.Name))
		for _, n := range o {
			set[n] = true
		}
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
	}
	var out []types.SkillInfo
	for _, n := range names {
		files, source, err := m.resolveRawForHat(ctx, userDB, hat.Name, n)
		if err != nil {
			continue
		}
		sk, err := buildSkill(n, files, source)
		if err != nil {
			continue
		}
		out = append(out, types.SkillInfo{Name: sk.Name, Description: sk.Description, Source: source})
	}
	return out, nil
}
