package skills

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/skills/jstmpl"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// Manager resolves skills from deployed files and DB overrides, rendering
// <?js ?> templates on read. Org-published overrides live in the shared
// database (held); per-user overrides live in the user's database (passed in).
type Manager struct {
	shared   *sql.DB
	skillDir string
	hatsDir  string
	runner   *jsrun.Runner
	cache    *fileCache
	// renderCtx builds a template context for a user (injected by the server).
	makeCtx func(userID int64, username, skill string) jstmpl.Context
}

// NewManager constructs a skills Manager bound to the shared database.
func NewManager(shared *sql.DB, skillDir, hatsDir string, runner *jsrun.Runner, makeCtx func(userID int64, username, skill string) jstmpl.Context) *Manager {
	return &Manager{shared: shared, skillDir: skillDir, hatsDir: hatsDir, runner: runner, cache: newFileCache(), makeCtx: makeCtx}
}

// deployedNames lists skill directories under skillDir.
func (m *Manager) deployedNames() ([]string, error) {
	return dirNames(m.skillDir)
}

// dirNames lists the immediate subdirectory names of root (sorted).
func dirNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// readDeployed reads a deployed skill's files from skillDir.
func (m *Manager) readDeployed(name string) (map[string]string, error) {
	return m.readDir(filepath.Join(m.skillDir, name))
}

// readDir reads all files under root into a relpath->contents map (cached by mtime).
func (m *Manager) readDir(root string) (map[string]string, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, err
	}
	files := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		raw, err := m.cache.read(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// readOverride loads a skill override's files from the given database (the
// shared database for org overrides, a user database for user overrides).
// Returns nil if absent.
func (m *Manager) readOverride(ctx context.Context, db *sql.DB, name string) (map[string]string, error) {
	if db == nil {
		return nil, nil
	}
	var overrideID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM skill_overrides WHERE skill_name = ?`, name).Scan(&overrideID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT relpath, content FROM skill_override_files WHERE override_id = ?`, overrideID)
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

// resolveRaw returns a skill's files and source: a user override wins over an
// org override, which wins over the deployed copy.
func (m *Manager) resolveRaw(ctx context.Context, userDB *sql.DB, name string) (map[string]string, string, error) {
	if files, err := m.readOverride(ctx, userDB, name); err != nil {
		return nil, "", err
	} else if files != nil {
		return files, "override", nil
	}
	if files, err := m.readOverride(ctx, m.shared, name); err != nil {
		return nil, "", err
	} else if files != nil {
		return files, "org", nil
	}
	files, err := m.readDeployed(name)
	if err != nil {
		return nil, "", err
	}
	return files, "deployed", nil
}

// Resolve returns the effective skill for a user, with templates rendered.
func (m *Manager) Resolve(ctx context.Context, userDB *sql.DB, name string, userID int64, username string) (*Skill, error) {
	files, source, err := m.resolveRaw(ctx, userDB, name)
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

// globalNames returns the union of deployed skill names and override names.
func (m *Manager) globalNames(ctx context.Context, userDB *sql.DB) ([]string, error) {
	nameSet := map[string]bool{}
	deployed, err := m.deployedNames()
	if err != nil {
		return nil, err
	}
	for _, n := range deployed {
		nameSet[n] = true
	}
	for _, db := range []*sql.DB{m.shared, userDB} {
		if db == nil {
			continue
		}
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT skill_name FROM skill_overrides`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			nameSet[n] = true
		}
		rows.Close()
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// ResolveRawFiles returns the effective skill files WITHOUT rendering (for download).
func (m *Manager) ResolveRawFiles(ctx context.Context, userDB *sql.DB, name string, userID int64) (map[string]string, string, error) {
	return m.resolveRaw(ctx, userDB, name)
}

// List returns skill info for all available skills for a user.
func (m *Manager) List(ctx context.Context, userDB *sql.DB, userID int64, username string) ([]types.SkillInfo, error) {
	names, err := m.globalNames(ctx, userDB)
	if err != nil {
		return nil, err
	}
	var out []types.SkillInfo
	for _, n := range names {
		files, source, err := m.resolveRaw(ctx, userDB, n)
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

// SaveOverride stores a user's override (replacing any existing one) in the
// user's database.
func (m *Manager) SaveOverride(ctx context.Context, userDB *sql.DB, name string, userID int64, files map[string]string) error {
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("override must include SKILL.md")
	}
	if _, _, err := parseFrontmatter(files["SKILL.md"]); err != nil {
		return fmt.Errorf("invalid SKILL.md: %w", err)
	}
	return m.writeOverride(ctx, userDB, name, nil, files)
}

// Publish promotes a skill version to an org default in the shared database.
func (m *Manager) Publish(ctx context.Context, name string, publishedBy int64, files map[string]string) error {
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("publish must include SKILL.md")
	}
	return m.writeOverride(ctx, m.shared, name, &publishedBy, files)
}

// writeOverride replaces the override for name in db. publishedBy is set for org
// (shared) overrides and nil for user overrides (whose table has no such column).
func (m *Manager) writeOverride(ctx context.Context, db *sql.DB, name string, publishedBy *int64, files map[string]string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, _ = tx.ExecContext(ctx, `DELETE FROM skill_overrides WHERE skill_name = ?`, name)

	var res sql.Result
	if publishedBy != nil {
		res, err = tx.ExecContext(ctx,
			`INSERT INTO skill_overrides(skill_name, published_by, updated_at) VALUES (?, ?, ?)`,
			name, *publishedBy, time.Now())
	} else {
		res, err = tx.ExecContext(ctx,
			`INSERT INTO skill_overrides(skill_name, updated_at) VALUES (?, ?)`,
			name, time.Now())
	}
	if err != nil {
		return err
	}
	overrideID, _ := res.LastInsertId()
	for rel, content := range files {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO skill_override_files(override_id, relpath, content) VALUES (?, ?, ?)`,
			overrideID, rel, []byte(content)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteOverride removes a user's override, reverting to org/deployed.
func (m *Manager) DeleteOverride(ctx context.Context, userDB *sql.DB, name string, userID int64) error {
	_, err := userDB.ExecContext(ctx, `DELETE FROM skill_overrides WHERE skill_name = ?`, name)
	return err
}
