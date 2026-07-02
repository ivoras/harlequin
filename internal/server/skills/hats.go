package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ivoras/harlequin/internal/shared/types"
	"gopkg.in/yaml.v3"
)

// hatPromptFile is the (optional) system prompt + metadata file of a hat.
const hatPromptFile = "system_prompt.md"

// hatSkillPrefix is the relpath prefix of per-hat skill overrides in hat_files:
// "skills/<skill>/<file>".
const hatSkillPrefix = "skills/"

// ErrHatNotFound is returned when the shared database has no such hat.
var ErrHatNotFound = errors.New("hat not found")

// hatFrontmatter is the optional YAML header of a hat's system_prompt.md. The
// skills list is the hat's visible-skill set (which skills are available while
// the hat is worn); an empty list means "all skills".
type hatFrontmatter struct {
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
}

// hatFromPrompt builds the Hat DTO from its (possibly empty) system_prompt.md.
func hatFromPrompt(name, raw string) types.Hat {
	h := types.Hat{Name: name}
	if raw != "" {
		fm, body := splitHatFrontmatter(raw)
		h.Description = fm.Description
		h.Skills = fm.Skills
		h.SystemPrompt = strings.TrimSpace(body)
	}
	return h
}

// ListHats returns the hats in the shared database (one query: the hat rows
// joined to their system_prompt.md, whose frontmatter carries the metadata).
func (m *Manager) ListHats(ctx context.Context) ([]types.Hat, error) {
	rows, err := m.shared.QueryContext(ctx,
		`SELECT h.name, COALESCE(f.content, '')
		 FROM hats h LEFT JOIN hat_files f ON f.hat_name = h.name AND f.relpath = ?
		 ORDER BY h.name`, hatPromptFile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.Hat
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		out = append(out, hatFromPrompt(name, string(raw)))
	}
	return out, rows.Err()
}

// GetHat reads a hat from the shared database: its optional system-prompt body
// and frontmatter (description + the skills it makes visible).
func (m *Manager) GetHat(ctx context.Context, name string) (*types.Hat, error) {
	files, err := readItemFiles(ctx, m.shared, hatTables, name)
	if err != nil {
		return nil, err
	}
	if files == nil {
		return nil, ErrHatNotFound
	}
	h := hatFromPrompt(name, files[hatPromptFile])
	return &h, nil
}

// SaveHat writes a hat (all its files) into the shared database, replacing any
// existing version. The description is taken from system_prompt.md frontmatter.
func (m *Manager) SaveHat(ctx context.Context, name string, userID int64, files map[string]string) error {
	fm, _ := splitHatFrontmatter(files[hatPromptFile])
	return writeItem(ctx, m.shared, hatTables, name, fm.Description, userID, files)
}

// DeleteHat removes a hat from the shared database.
func (m *Manager) DeleteHat(ctx context.Context, name string) error {
	_, err := m.shared.ExecContext(ctx, `DELETE FROM hats WHERE name = ?`, name)
	return err
}

// ImportHatsFromDisk one-time-imports hats from the legacy on-disk directory
// (<data_dir>/hats/<name>/...) into the shared database. Only hats the database
// does not already hold are imported, so local edits made on disk survive the
// storage switch; the directory itself is left untouched.
func ImportHatsFromDisk(ctx context.Context, shared *sql.DB, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		var exists int
		if err := shared.QueryRowContext(ctx, `SELECT COUNT(*) FROM hats WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		root := filepath.Join(dir, name)
		files := map[string]string{}
		err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = string(raw)
			return nil
		})
		if err != nil {
			return fmt.Errorf("import hat %q: %w", name, err)
		}
		fm, _ := splitHatFrontmatter(files[hatPromptFile])
		if err := writeItem(ctx, shared, hatTables, name, fm.Description, 0, files); err != nil {
			return fmt.Errorf("import hat %q: %w", name, err)
		}
	}
	return nil
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

// escapeLike escapes SQLite LIKE metacharacters so a literal string can be
// used inside a pattern (paired with ESCAPE '\'). Without it, '_'/'%' in a
// skill name would wildcard-match other skills' rows.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// hatOverrideFiles returns a hat's override files for one skill (relpaths
// relative to the skill, i.e. with the "skills/<name>/" prefix stripped), or
// nil when the hat has no override for that skill.
func (m *Manager) hatOverrideFiles(ctx context.Context, hat, skill string) (map[string]string, error) {
	prefix := hatSkillPrefix + skill + "/"
	rows, err := m.shared.QueryContext(ctx,
		`SELECT relpath, content FROM hat_files WHERE hat_name = ? AND relpath LIKE ? ESCAPE '\'`,
		hat, escapeLike(prefix)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files map[string]string
	for rows.Next() {
		var rel string
		var content []byte
		if err := rows.Scan(&rel, &content); err != nil {
			return nil, err
		}
		if files == nil {
			files = map[string]string{}
		}
		files[strings.TrimPrefix(rel, prefix)] = string(content)
	}
	return files, rows.Err()
}

// hatOverrideInfos returns, in one query, the skills a hat carries overrides
// for, mapped to the override's SKILL.md description. Overrides replace the
// whole skill wholesale, so an override without a SKILL.md marks the skill
// unusable while the hat is worn (present in the map with ok=false).
type hatOverride struct {
	description string
	hasSkillMD  bool
}

func (m *Manager) hatOverrideInfos(ctx context.Context, hat string) (map[string]hatOverride, error) {
	rows, err := m.shared.QueryContext(ctx,
		`SELECT relpath, content FROM hat_files WHERE hat_name = ? AND relpath LIKE ?`,
		hat, hatSkillPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hatOverride{}
	for rows.Next() {
		var rel string
		var content []byte
		if err := rows.Scan(&rel, &content); err != nil {
			return nil, err
		}
		name, sub, ok := strings.Cut(strings.TrimPrefix(rel, hatSkillPrefix), "/")
		if !ok || name == "" {
			continue
		}
		o := out[name]
		if sub == "SKILL.md" {
			o.hasSkillMD = true
			if fm, _, err := parseFrontmatter(string(content)); err == nil {
				o.description = fm.Description
			}
		}
		out[name] = o
	}
	return out, rows.Err()
}

// resolveRawForHat resolves a skill's raw files while a hat is worn: a per-hat
// override (hat_files "skills/<name>/...") wins; otherwise normal resolution
// applies.
func (m *Manager) resolveRawForHat(ctx context.Context, userDB, projDB *sql.DB, hat, name string) (map[string]string, string, error) {
	files, err := m.hatOverrideFiles(ctx, hat, name)
	if err != nil {
		return nil, "", err
	}
	if files != nil {
		return files, "hat", nil
	}
	return m.resolveRaw(ctx, userDB, projDB, name)
}

// ResolveEffective resolves a skill honoring the worn hat's overrides. hat may
// be nil (normal resolution).
func (m *Manager) ResolveEffective(ctx context.Context, userDB, projDB *sql.DB, name string, userID int64, username string, hat *types.Hat) (*Skill, error) {
	if hat == nil {
		return m.Resolve(ctx, userDB, projDB, name, userID, username)
	}
	files, source, err := m.resolveRawForHat(ctx, userDB, projDB, hat.Name, name)
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
//
// Like List, this reads only names + stored descriptions (plus one query for
// the hat's overrides), never the file blobs.
func (m *Manager) EffectiveSkillInfos(ctx context.Context, userDB, projDB *sql.DB, hat *types.Hat) ([]types.SkillInfo, error) {
	base, err := m.List(ctx, userDB, projDB)
	if hat == nil || err != nil {
		return base, err
	}
	overrides, err := m.hatOverrideInfos(ctx, hat.Name)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]types.SkillInfo, len(base))
	for _, i := range base {
		byName[i.Name] = i
	}
	for n, o := range overrides {
		if !o.hasSkillMD {
			// Overrides replace a skill wholesale; without a SKILL.md the skill
			// is unresolvable under this hat, so hide it (matches ResolveEffective).
			delete(byName, n)
			continue
		}
		byName[n] = types.SkillInfo{Name: n, Description: o.description, Source: "hat"}
	}
	names := hat.Skills
	if len(names) == 0 {
		names = make([]string, 0, len(byName))
		for n := range byName {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	var out []types.SkillInfo
	for _, n := range names {
		if info, ok := byName[n]; ok {
			out = append(out, info)
		}
	}
	return out, nil
}
