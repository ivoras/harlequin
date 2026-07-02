package skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Scope labels for skills. Resolution precedence is project → shared → user
// (mirrors the documents corpus order): the "deeper" scope wins.
const (
	ScopeProject = "project"
	ScopeShared  = "shared"
	ScopeUser    = "user"
)

// ErrSkillNotFound is returned when no scope holds the named skill.
var ErrSkillNotFound = errors.New("skill not found")

// Manager resolves skills from the scoped databases (project → shared → user)
// and renders <?js ?> templates on read. Baked-in skills and hats are seeded
// into the shared database from the server binary.
type Manager struct {
	shared *sql.DB
	baked  fs.FS // embedded asset tree; skills live under "skills/", hats under "hats/"
	runner *jsrun.Runner
	// renderCtx builds a template context for a user (injected by the server).
	makeCtx func(userID int64, username, skill string) jstmpl.Context
}

// NewManager constructs a skills Manager bound to the shared database. baked is
// the embedded asset FS (skills under "skills/", hats under "hats/"), used to
// render the system prompt and to seed shared skills and hats.
func NewManager(shared *sql.DB, baked fs.FS, runner *jsrun.Runner, makeCtx func(userID int64, username, skill string) jstmpl.Context) *Manager {
	return &Manager{shared: shared, baked: baked, runner: runner, makeCtx: makeCtx}
}

// scopeDB pairs a database with its scope label.
type scopeDB struct {
	scope string
	db    *sql.DB
}

// scopeDBs returns the ordered resolution list: project, shared, user. nil DBs
// are skipped (projDB nil outside a project session, userDB nil in server-only
// contexts).
func (m *Manager) scopeDBs(userDB, projDB *sql.DB) []scopeDB {
	out := make([]scopeDB, 0, 3)
	if projDB != nil {
		out = append(out, scopeDB{ScopeProject, projDB})
	}
	out = append(out, scopeDB{ScopeShared, m.shared})
	if userDB != nil {
		out = append(out, scopeDB{ScopeUser, userDB})
	}
	return out
}

// readSkillFiles reads one skill's files from a single database (relpath ->
// contents). Returns nil (no error) when the database has no such skill.
func readSkillFiles(ctx context.Context, db *sql.DB, name string) (map[string]string, error) {
	if db == nil {
		return nil, nil
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skills WHERE name = ?`, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT relpath, content FROM skill_files WHERE skill_name = ?`, name)
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

// resolveRaw returns a skill's files and the scope it resolved from, honoring
// project → shared → user precedence.
func (m *Manager) resolveRaw(ctx context.Context, userDB, projDB *sql.DB, name string) (map[string]string, string, error) {
	for _, sc := range m.scopeDBs(userDB, projDB) {
		files, err := readSkillFiles(ctx, sc.db, name)
		if err != nil {
			return nil, "", err
		}
		if files != nil {
			return files, sc.scope, nil
		}
	}
	return nil, "", ErrSkillNotFound
}

// Resolve returns the effective skill for a user (project → shared → user), with
// templates rendered.
func (m *Manager) Resolve(ctx context.Context, userDB, projDB *sql.DB, name string, userID int64, username string) (*Skill, error) {
	files, source, err := m.resolveRaw(ctx, userDB, projDB, name)
	if err != nil {
		return nil, err
	}
	rendered, err := m.renderFiles(files, name, userID, username)
	if err != nil {
		return nil, err
	}
	return buildSkill(name, rendered, source)
}

// renderFiles renders .md/.txt files through the template engine, copying others
// verbatim.
func (m *Manager) renderFiles(files map[string]string, name string, userID int64, username string) (map[string]string, error) {
	rendered := make(map[string]string, len(files))
	for rel, content := range files {
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, ".txt") {
			r, err := jstmpl.Render(m.runner, content, m.makeCtx(userID, username, name))
			if err != nil {
				return nil, fmt.Errorf("render %s/%s: %w", name, rel, err)
			}
			rendered[rel] = r
		} else {
			rendered[rel] = content
		}
	}
	return rendered, nil
}

// allNames returns the union of skill names across the resolvable scopes (sorted).
func (m *Manager) allNames(ctx context.Context, userDB, projDB *sql.DB) ([]string, error) {
	set := map[string]bool{}
	for _, sc := range m.scopeDBs(userDB, projDB) {
		if sc.db == nil {
			continue
		}
		rows, err := sc.db.QueryContext(ctx, `SELECT name FROM skills`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			set[n] = true
		}
		rows.Close()
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// ResolveRawFiles returns the effective skill files WITHOUT rendering (for
// download/editing) plus the scope it resolved from.
func (m *Manager) ResolveRawFiles(ctx context.Context, userDB, projDB *sql.DB, name string) (map[string]string, string, error) {
	return m.resolveRaw(ctx, userDB, projDB, name)
}

// GetFile returns one file of the effective skill (project → shared → user).
func (m *Manager) GetFile(ctx context.Context, userDB, projDB *sql.DB, name, relpath string) (string, string, error) {
	files, source, err := m.resolveRaw(ctx, userDB, projDB, name)
	if err != nil {
		return "", "", err
	}
	content, ok := files[relpath]
	if !ok {
		return "", "", fmt.Errorf("skill %q has no file %q", name, relpath)
	}
	return content, source, nil
}

// List returns skill info for every skill visible to a user (across scopes),
// each tagged with the scope it resolves from.
func (m *Manager) List(ctx context.Context, userDB, projDB *sql.DB, userID int64, username string) ([]types.SkillInfo, error) {
	names, err := m.allNames(ctx, userDB, projDB)
	if err != nil {
		return nil, err
	}
	var out []types.SkillInfo
	for _, n := range names {
		files, source, err := m.resolveRaw(ctx, userDB, projDB, n)
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

// ScopeDBFor picks, from the scope label, which database a write targets. An
// empty label defaults to project when a project session is active, else user.
// The shared database is always m.shared (org-wide).
func (m *Manager) ScopeDBFor(scope string, userDB, projDB *sql.DB) (*sql.DB, string, error) {
	if scope == "" {
		if projDB != nil {
			scope = ScopeProject
		} else {
			scope = ScopeUser
		}
	}
	switch scope {
	case ScopeProject:
		if projDB == nil {
			return nil, "", fmt.Errorf("no active project for project-scoped skill")
		}
		return projDB, ScopeProject, nil
	case ScopeShared:
		return m.shared, ScopeShared, nil
	case ScopeUser:
		if userDB == nil {
			return nil, "", fmt.Errorf("no user database for user-scoped skill")
		}
		return userDB, ScopeUser, nil
	default:
		return nil, "", fmt.Errorf("unknown skill scope %q", scope)
	}
}

// Save writes a skill (all its files) into a specific scope database, replacing
// any existing version there. It requires a parseable SKILL.md.
func (m *Manager) Save(ctx context.Context, db *sql.DB, name string, userID int64, files map[string]string) error {
	md, ok := files["SKILL.md"]
	if !ok {
		return fmt.Errorf("skill %q must include SKILL.md", name)
	}
	fm, _, err := parseFrontmatter(md)
	if err != nil {
		return fmt.Errorf("invalid SKILL.md: %w", err)
	}
	return writeSkill(ctx, db, name, fm.Description, userID, files)
}

// writeSkill upserts a skill row and replaces its files, in one transaction.
func writeSkill(ctx context.Context, db *sql.DB, name, description string, userID int64, files map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO skills(name, description, updated_by, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET description = excluded.description,
		   updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		name, description, userID, time.Now()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM skill_files WHERE skill_name = ?`, name); err != nil {
		return err
	}
	for rel, content := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO skill_files(skill_name, relpath, content) VALUES (?, ?, ?)`,
			name, rel, []byte(content)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Create scaffolds a new skill (a minimal SKILL.md) in the given scope database.
func (m *Manager) Create(ctx context.Context, db *sql.DB, name, description string, userID int64) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skills WHERE name = ?`, name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return fmt.Errorf("skill %q already exists in this scope", name)
	}
	return m.Save(ctx, db, name, userID, map[string]string{"SKILL.md": ScaffoldSkill(name, description)})
}

// PutFile writes a single file into a skill within the target scope database. If
// the target scope does not yet hold the skill, the effective version (from any
// scope) is copied in first so the scope owns a complete, valid skill.
func (m *Manager) PutFile(ctx context.Context, db, userDB, projDB *sql.DB, name, relpath, content string, userID int64) error {
	files, err := readSkillFiles(ctx, db, name)
	if err != nil {
		return err
	}
	if files == nil {
		if eff, _, err := m.resolveRaw(ctx, userDB, projDB, name); err == nil {
			files = eff
		} else {
			files = map[string]string{}
		}
	}
	files[relpath] = content
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("skill %q has no SKILL.md", name)
	}
	return m.Save(ctx, db, name, userID, files)
}

// Delete removes a skill from a specific scope database.
func (m *Manager) Delete(ctx context.Context, db *sql.DB, name string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM skills WHERE name = ?`, name)
	return err
}

// ScaffoldSkill returns a minimal SKILL.md for a new skill.
func ScaffoldSkill(name, description string) string {
	if strings.TrimSpace(description) == "" {
		description = "TODO describe when this skill should be used."
	}
	return strings.Join([]string{
		"---",
		"name: " + name,
		"description: " + description,
		"---",
		"# " + name,
		"",
		"TODO: write the skill instructions here. You can use <?js print(ctx.user); ?> for dynamic content.",
		"",
	}, "\n")
}
