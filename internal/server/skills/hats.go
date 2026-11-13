package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/shared/types"
	"gopkg.in/yaml.v3"
)

// hatPromptFile is the (optional) system prompt + metadata file of a hat.
const hatPromptFile = "system_prompt.md"

// hatSkillPrefix is the relpath prefix of per-hat skill overrides in hat_files:
// "skills/<skill>/<file>".
const hatSkillPrefix = "skills/"

// ErrHatNotFound is returned when no shared or personal hat has the name.
var ErrHatNotFound = errors.New("hat not found")

// ErrPersonalHatInProject is returned when a personal hat is put on a project
// session: the session is shared and other members' turns cannot read the
// wearer's user.db.
var ErrPersonalHatInProject = errors.New("a personal hat can't be worn in a project session (other members can't read it); use a shared hat")

// hatFrontmatter is the optional YAML header of a hat's system_prompt.md. The
// skills list is the hat's visible-skill set (which skills are available while
// the hat is worn); an empty list means "all skills".
type hatFrontmatter struct {
	Description string   `yaml:"description"`
	Skills      []string `yaml:"skills"`
	// UsePrompt gates the custom system prompt (the file body): false keeps
	// the body around but the default system prompt is used while worn.
	// nil/true = body active when non-empty.
	UsePrompt *bool `yaml:"use_prompt"`
	// ReplacePrompt makes the body replace the default system prompt instead
	// of being appended to it.
	ReplacePrompt bool `yaml:"replace_prompt"`
}

// hatFromPrompt builds the Hat DTO from its (possibly empty) system_prompt.md.
func hatFromPrompt(name, raw string) types.Hat {
	h := types.Hat{Name: name}
	if raw != "" {
		fm, body := splitHatFrontmatter(raw)
		h.Description = fm.Description
		h.Skills = fm.Skills
		h.HasCustomPrompt = strings.TrimSpace(body) != ""
		h.ReplacePrompt = fm.ReplacePrompt
		h.PromptDisabled = fm.UsePrompt != nil && !*fm.UsePrompt
		if !h.PromptDisabled {
			// A disabled custom prompt is kept in the file but not served, so
			// the agent (and clients) fall back to the default system prompt.
			h.SystemPrompt = strings.TrimSpace(body)
		}
	}
	return h
}

// overlaySkillNames derives the overlay skill names from a hat's file relpaths.
func overlaySkillNames(files map[string]string) []string {
	set := map[string]bool{}
	for rel := range files {
		name, _, ok := strings.Cut(strings.TrimPrefix(rel, hatSkillPrefix), "/")
		if ok && name != "" && strings.HasPrefix(rel, hatSkillPrefix) {
			set[name] = true
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ListHats returns every hat the user can wear: the shared ones plus their own
// personal ones (userDB may be nil). A shared hat shadows a personal one of the
// same name, so each name appears once.
func (m *Manager) ListHats(ctx context.Context, userDB *sql.DB) ([]types.Hat, error) {
	out, err := listHatsIn(ctx, m.shared, types.HatScopeShared)
	if err != nil {
		return nil, err
	}
	if userDB == nil {
		return out, nil
	}
	mine, err := listHatsIn(ctx, userDB, types.HatScopePersonal)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, h := range out {
		taken[h.Name] = true
	}
	for _, h := range mine {
		if !taken[h.Name] {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// listHatsIn lists one database's hats (one query: the hat rows joined to their
// system_prompt.md, whose frontmatter carries the metadata).
func listHatsIn(ctx context.Context, db *sql.DB, scope string) ([]types.Hat, error) {
	rows, err := db.QueryContext(ctx,
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
		h := hatFromPrompt(name, string(raw))
		h.Scope = scope
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Attach each hat's overlay-skill names (one grouped query for all hats).
	ov, err := db.QueryContext(ctx,
		`SELECT hat_name, relpath FROM hat_files WHERE relpath LIKE ?`, hatSkillPrefix+"%")
	if err != nil {
		return nil, err
	}
	defer ov.Close()
	byHat := map[string]map[string]string{}
	for ov.Next() {
		var hat, rel string
		if err := ov.Scan(&hat, &rel); err != nil {
			return nil, err
		}
		if byHat[hat] == nil {
			byHat[hat] = map[string]string{}
		}
		byHat[hat][rel] = ""
	}
	if err := ov.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].OverlaySkills = overlaySkillNames(byHat[out[i].Name])
	}
	return out, nil
}

func hatExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	if db == nil {
		return false, nil
	}
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hats WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// HatHome finds the database holding the named hat and its scope: shared first
// (a shared hat shadows a personal one), then the user's own. ErrHatNotFound
// when neither has it.
func (m *Manager) HatHome(ctx context.Context, userDB *sql.DB, name string) (*sql.DB, string, error) {
	if ok, err := hatExists(ctx, m.shared, name); err != nil {
		return nil, "", err
	} else if ok {
		return m.shared, types.HatScopeShared, nil
	}
	if ok, err := hatExists(ctx, userDB, name); err != nil {
		return nil, "", err
	} else if ok {
		return userDB, types.HatScopePersonal, nil
	}
	return nil, "", ErrHatNotFound
}

// GetHat reads a hat (shared, else the user's own): its optional system-prompt
// body and frontmatter (description + the skills it makes visible).
func (m *Manager) GetHat(ctx context.Context, userDB *sql.DB, name string) (*types.Hat, error) {
	db, scope, err := m.HatHome(ctx, userDB, name)
	if err != nil {
		return nil, err
	}
	files, err := readItemFiles(ctx, db, hatTables, name)
	if err != nil {
		return nil, err
	}
	if files == nil {
		return nil, ErrHatNotFound
	}
	h := hatFromPrompt(name, files[hatPromptFile])
	h.OverlaySkills = overlaySkillNames(files)
	h.Scope = scope
	return &h, nil
}

// SaveHat writes a hat (all its files) into db, replacing any existing version.
// The description is taken from system_prompt.md frontmatter.
func (m *Manager) SaveHat(ctx context.Context, db *sql.DB, name string, userID int64, files map[string]string) error {
	fm, _ := splitHatFrontmatter(files[hatPromptFile])
	return writeItem(ctx, db, hatTables, name, fm.Description, userID, files)
}

// CreateHat scaffolds a new hat in the shared database (shared) or userDB
// (personal): a system_prompt.md with frontmatter and an empty body. A name
// that exists as a shared hat is refused in either scope, so a personal hat can
// never be shadowed from day one.
func (m *Manager) CreateHat(ctx context.Context, userDB *sql.DB, scope, name, description string, userID int64) error {
	db := m.shared
	if scope == types.HatScopePersonal {
		db = userDB
	} else if scope != types.HatScopeShared {
		return fmt.Errorf("scope must be %q or %q", types.HatScopeShared, types.HatScopePersonal)
	}
	if db == nil {
		return fmt.Errorf("no database for %s hats", scope)
	}
	if _, _, err := m.HatHome(ctx, userDB, name); err == nil {
		return fmt.Errorf("hat %q already exists", name)
	} else if !errors.Is(err, ErrHatNotFound) {
		return err
	}
	if strings.TrimSpace(description) == "" {
		description = "TODO describe what kind of work this hat is for."
	}
	md := strings.Join([]string{
		"---",
		"description: " + description,
		"# skills: [name, ...] restricts which skills are visible while worn (empty = all).",
		"skills: []",
		"# The body below is added to the default system prompt. Set replace_prompt: true to",
		"# replace the default prompt instead (you then lose its citation/document/memory rules).",
		"# replace_prompt: true",
		"---",
		"",
	}, "\n")
	return writeItem(ctx, db, hatTables, name, description, userID, map[string]string{hatPromptFile: md})
}

// GetHatFiles returns a hat's raw files (relpath -> content) from db.
func (m *Manager) GetHatFiles(ctx context.Context, db *sql.DB, name string) (map[string]string, error) {
	files, err := readItemFiles(ctx, db, hatTables, name)
	if err != nil {
		return nil, err
	}
	if files == nil {
		return nil, ErrHatNotFound
	}
	return files, nil
}

// PutHatFile writes one file of a hat (e.g. system_prompt.md, or an overlay
// file under skills/<skill>/...).
func (m *Manager) PutHatFile(ctx context.Context, db *sql.DB, name, relpath, content string, userID int64) error {
	files, err := m.GetHatFiles(ctx, db, name)
	if err != nil {
		return err
	}
	files[relpath] = content
	return m.SaveHat(ctx, db, name, userID, files)
}

// AddHatSkill copies the currently-resolved skill into the hat's overlay
// (skills/<skill>/...), so the hat carries its own editable variant that takes
// precedence over normal resolution while the hat is worn. db is the hat's home.
func (m *Manager) AddHatSkill(ctx context.Context, db, userDB, projDB *sql.DB, hat, skill string, userID int64) error {
	files, err := m.GetHatFiles(ctx, db, hat)
	if err != nil {
		return err
	}
	src, _, err := m.resolveRaw(ctx, userDB, projDB, skill)
	if err != nil {
		return err
	}
	for rel, content := range src {
		files[hatSkillPrefix+skill+"/"+rel] = content
	}
	return m.SaveHat(ctx, db, hat, userID, files)
}

// SetHatPromptEnabled toggles the hat's custom system prompt via the
// use_prompt frontmatter flag — a textual patch that preserves the body (and
// any comments), so disabling never loses the prompt's content.
func (m *Manager) SetHatPromptEnabled(ctx context.Context, db *sql.DB, name string, enabled bool, userID int64) error {
	files, err := m.GetHatFiles(ctx, db, name)
	if err != nil {
		return err
	}
	files[hatPromptFile] = setUsePromptFlag(files[hatPromptFile], enabled)
	return m.SaveHat(ctx, db, name, userID, files)
}

// setUsePromptFlag sets/replaces the use_prompt line in the YAML frontmatter,
// creating a frontmatter block if the file has none.
func setUsePromptFlag(content string, enabled bool) string {
	line := "use_prompt: " + strconv.FormatBool(enabled)
	trimmed := strings.TrimLeft(content, "\ufeff \t\r\n")
	if strings.HasPrefix(trimmed, "---") {
		rest := trimmed[3:]
		if idx := strings.Index(rest, "\n---"); idx >= 0 {
			header := rest[:idx]
			lines := strings.Split(header, "\n")
			replaced := false
			for i, l := range lines {
				if strings.HasPrefix(strings.TrimSpace(l), "use_prompt:") {
					lines[i] = line
					replaced = true
					break
				}
			}
			if !replaced {
				lines = append(lines, line)
			}
			return "---" + strings.Join(lines, "\n") + rest[idx:]
		}
	}
	return "---\n" + line + "\n---\n" + content
}

// RemoveHatSkill drops a skill's overlay from the hat (the skill then resolves
// normally again while the hat is worn).
func (m *Manager) RemoveHatSkill(ctx context.Context, db *sql.DB, hat, skill string) error {
	prefix := hatSkillPrefix + skill + "/"
	_, err := db.ExecContext(ctx,
		`DELETE FROM hat_files WHERE hat_name = ? AND relpath LIKE ? ESCAPE '\'`,
		hat, escapeLike(prefix)+"%")
	return err
}

// DeleteHat removes a hat from db (its home).
func (m *Manager) DeleteHat(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM hats WHERE name = ?`, name)
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
func hatOverrideFiles(ctx context.Context, db *sql.DB, hat, skill string) (map[string]string, error) {
	prefix := hatSkillPrefix + skill + "/"
	rows, err := db.QueryContext(ctx,
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

func hatOverrideInfos(ctx context.Context, db *sql.DB, hat string) (map[string]hatOverride, error) {
	rows, err := db.QueryContext(ctx,
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
func (m *Manager) resolveRawForHat(ctx context.Context, userDB, projDB *sql.DB, hat *types.Hat, name string) (map[string]string, string, error) {
	files, err := hatOverrideFiles(ctx, m.hatDB(hat, userDB), hat.Name, name)
	if err != nil {
		return nil, "", err
	}
	if files != nil {
		return files, "hat", nil
	}
	return m.resolveRaw(ctx, userDB, projDB, name)
}

// hatDB is the database a worn hat's overlay files live in: the user's for a
// personal hat, else the shared one.
func (m *Manager) hatDB(hat *types.Hat, userDB *sql.DB) *sql.DB {
	if hat.Scope == types.HatScopePersonal {
		return userDB
	}
	return m.shared
}

// ResolveEffective resolves a skill honoring the worn hat's overrides. hat may
// be nil (normal resolution).
func (m *Manager) ResolveEffective(ctx context.Context, userDB, projDB *sql.DB, name string, userID int64, username string, hat *types.Hat) (*Skill, error) {
	if hat == nil {
		return m.Resolve(ctx, userDB, projDB, name, userID, username)
	}
	files, source, err := m.resolveRawForHat(ctx, userDB, projDB, hat, name)
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
	overrides, err := hatOverrideInfos(ctx, m.hatDB(hat, userDB), hat.Name)
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
		info := types.SkillInfo{Name: n, Description: o.description, Source: "hat"}
		if prev, ok := byName[n]; ok {
			info.AlsoIn = append([]string{prev.Source}, prev.AlsoIn...)
		}
		byName[n] = info
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
