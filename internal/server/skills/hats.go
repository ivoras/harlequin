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
	"time"

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

// readHatFiles reads one hat's files from the shared database (relpath ->
// contents). Returns nil (no error) when there is no such hat.
func (m *Manager) readHatFiles(ctx context.Context, name string) (map[string]string, error) {
	var exists int
	if err := m.shared.QueryRowContext(ctx, `SELECT COUNT(*) FROM hats WHERE name = ?`, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := m.shared.QueryContext(ctx, `SELECT relpath, content FROM hat_files WHERE hat_name = ?`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := map[string]string{}
	for rows.Next() {
		var rel string
		var content []byte
		if err := rows.Scan(&rel, &content); err != nil {
			return nil, err
		}
		files[rel] = string(content)
	}
	return files, rows.Err()
}

// ListHats returns the hats in the shared database.
func (m *Manager) ListHats(ctx context.Context) ([]types.Hat, error) {
	rows, err := m.shared.QueryContext(ctx, `SELECT name FROM hats ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []types.Hat
	for _, n := range names {
		h, err := m.GetHat(ctx, n)
		if err != nil {
			continue
		}
		out = append(out, *h)
	}
	return out, nil
}

// GetHat reads a hat from the shared database: its optional system-prompt body
// and frontmatter (description + the skills it makes visible).
func (m *Manager) GetHat(ctx context.Context, name string) (*types.Hat, error) {
	files, err := m.readHatFiles(ctx, name)
	if err != nil {
		return nil, err
	}
	if files == nil {
		return nil, ErrHatNotFound
	}
	h := &types.Hat{Name: name}
	if raw, ok := files[hatPromptFile]; ok {
		fm, body := splitHatFrontmatter(raw)
		h.Description = fm.Description
		h.Skills = fm.Skills
		h.SystemPrompt = strings.TrimSpace(body)
	}
	return h, nil
}

// SaveHat writes a hat (all its files) into the shared database, replacing any
// existing version. The description is taken from system_prompt.md frontmatter.
func (m *Manager) SaveHat(ctx context.Context, name string, userID int64, files map[string]string) error {
	fm, _ := splitHatFrontmatter(files[hatPromptFile])
	return writeHat(ctx, m.shared, name, fm.Description, userID, files)
}

// DeleteHat removes a hat from the shared database.
func (m *Manager) DeleteHat(ctx context.Context, name string) error {
	_, err := m.shared.ExecContext(ctx, `DELETE FROM hats WHERE name = ?`, name)
	return err
}

// writeHat upserts a hat row and replaces its files, in one transaction
// (mirrors writeSkill).
func writeHat(ctx context.Context, db *sql.DB, name, description string, userID int64, files map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO hats(name, description, updated_by, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET description = excluded.description,
		   updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		name, description, userID, time.Now()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hat_files WHERE hat_name = ?`, name); err != nil {
		return err
	}
	for rel, content := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO hat_files(hat_name, relpath, content) VALUES (?, ?, ?)`,
			name, rel, []byte(content)); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		if err := writeHat(ctx, shared, name, fm.Description, 0, files); err != nil {
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

// hatOverrideFiles returns a hat's override files for one skill (relpaths
// relative to the skill, i.e. with the "skills/<name>/" prefix stripped), or
// nil when the hat has no override for that skill.
func (m *Manager) hatOverrideFiles(ctx context.Context, hat, skill string) (map[string]string, error) {
	prefix := hatSkillPrefix + skill + "/"
	rows, err := m.shared.QueryContext(ctx,
		`SELECT relpath, content FROM hat_files WHERE hat_name = ? AND relpath LIKE ?`,
		hat, prefix+"%")
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

// hatOverrideSkillNames returns the names of skills a hat carries overrides for.
func (m *Manager) hatOverrideSkillNames(ctx context.Context, hat string) ([]string, error) {
	rows, err := m.shared.QueryContext(ctx,
		`SELECT DISTINCT relpath FROM hat_files WHERE hat_name = ? AND relpath LIKE ?`,
		hat, hatSkillPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return nil, err
		}
		name, _, ok := strings.Cut(strings.TrimPrefix(rel, hatSkillPrefix), "/")
		if ok && name != "" {
			set[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
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
func (m *Manager) EffectiveSkillInfos(ctx context.Context, userDB, projDB *sql.DB, userID int64, username string, hat *types.Hat) ([]types.SkillInfo, error) {
	if hat == nil {
		return m.List(ctx, userDB, projDB, userID, username)
	}
	names := hat.Skills
	if len(names) == 0 {
		set := map[string]bool{}
		g, err := m.allNames(ctx, userDB, projDB)
		if err != nil {
			return nil, err
		}
		for _, n := range g {
			set[n] = true
		}
		o, _ := m.hatOverrideSkillNames(ctx, hat.Name)
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
		files, source, err := m.resolveRawForHat(ctx, userDB, projDB, hat.Name, n)
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
