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
// <?js ?> templates on read.
type Manager struct {
	db       *sql.DB
	skillDir string
	runner   *jsrun.Runner
	// renderCtx builds a template context for a user (injected by the server).
	makeCtx func(userID int64, username, skill string) jstmpl.Context
}

// NewManager constructs a skills Manager.
func NewManager(db *sql.DB, skillDir string, runner *jsrun.Runner, makeCtx func(userID int64, username, skill string) jstmpl.Context) *Manager {
	return &Manager{db: db, skillDir: skillDir, runner: runner, makeCtx: makeCtx}
}

// deployedNames lists skill directories under skillDir.
func (m *Manager) deployedNames() ([]string, error) {
	entries, err := os.ReadDir(m.skillDir)
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

// readDeployed reads a deployed skill's files from disk.
func (m *Manager) readDeployed(name string) (map[string]string, error) {
	root := filepath.Join(m.skillDir, name)
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
		raw, err := os.ReadFile(p)
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

// readOverride loads an override's files for a given scope/user. Returns nil if absent.
func (m *Manager) readOverride(ctx context.Context, name string, userID *int64, scope string) (map[string]string, error) {
	var overrideID int64
	var err error
	if scope == "org" {
		err = m.db.QueryRowContext(ctx,
			`SELECT id FROM skill_overrides WHERE skill_name = ? AND scope = 'org'`, name).Scan(&overrideID)
	} else {
		err = m.db.QueryRowContext(ctx,
			`SELECT id FROM skill_overrides WHERE skill_name = ? AND scope = 'user' AND user_id = ?`, name, *userID).Scan(&overrideID)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT relpath, content FROM skill_override_files WHERE override_id = ?`, overrideID)
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

// resolveRaw returns a skill's files and source (without rendering templates).
func (m *Manager) resolveRaw(ctx context.Context, name string, userID int64) (map[string]string, string, error) {
	if files, err := m.readOverride(ctx, name, &userID, "user"); err != nil {
		return nil, "", err
	} else if files != nil {
		return files, "override", nil
	}
	if files, err := m.readOverride(ctx, name, nil, "org"); err != nil {
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
func (m *Manager) Resolve(ctx context.Context, name string, userID int64, username string) (*Skill, error) {
	files, source, err := m.resolveRaw(ctx, name, userID)
	if err != nil {
		return nil, err
	}
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
	return buildSkill(name, rendered, source)
}

// ResolveRawFiles returns the effective skill files WITHOUT rendering (for download).
func (m *Manager) ResolveRawFiles(ctx context.Context, name string, userID int64) (map[string]string, string, error) {
	return m.resolveRaw(ctx, name, userID)
}

// List returns skill info for all available skills for a user.
func (m *Manager) List(ctx context.Context, userID int64, username string) ([]types.SkillInfo, error) {
	// Union of deployed names and any org/user override names.
	nameSet := map[string]bool{}
	deployed, err := m.deployedNames()
	if err != nil {
		return nil, err
	}
	for _, n := range deployed {
		nameSet[n] = true
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT DISTINCT skill_name FROM skill_overrides WHERE scope = 'org' OR (scope = 'user' AND user_id = ?)`, userID)
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

	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []types.SkillInfo
	for _, n := range names {
		files, source, err := m.resolveRaw(ctx, n, userID)
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

// SaveOverride stores a user's override (replacing any existing one).
func (m *Manager) SaveOverride(ctx context.Context, name string, userID int64, files map[string]string) error {
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("override must include SKILL.md")
	}
	if _, _, err := parseFrontmatter(files["SKILL.md"]); err != nil {
		return fmt.Errorf("invalid SKILL.md: %w", err)
	}
	return m.writeOverride(ctx, name, &userID, "user", nil, files)
}

// Publish promotes a user's (or arbitrary) skill version to an org default.
func (m *Manager) Publish(ctx context.Context, name string, publishedBy int64, files map[string]string) error {
	if _, ok := files["SKILL.md"]; !ok {
		return fmt.Errorf("publish must include SKILL.md")
	}
	return m.writeOverride(ctx, name, nil, "org", &publishedBy, files)
}

func (m *Manager) writeOverride(ctx context.Context, name string, userID *int64, scope string, publishedBy *int64, files map[string]string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete existing override of this scope.
	if scope == "org" {
		_, _ = tx.ExecContext(ctx, `DELETE FROM skill_overrides WHERE skill_name = ? AND scope = 'org'`, name)
	} else {
		_, _ = tx.ExecContext(ctx, `DELETE FROM skill_overrides WHERE skill_name = ? AND scope = 'user' AND user_id = ?`, name, *userID)
	}

	var uid, pby any
	if userID != nil {
		uid = *userID
	}
	if publishedBy != nil {
		pby = *publishedBy
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO skill_overrides(user_id, skill_name, scope, published_by, updated_at) VALUES (?, ?, ?, ?, ?)`,
		uid, name, scope, pby, time.Now())
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
func (m *Manager) DeleteOverride(ctx context.Context, name string, userID int64) error {
	_, err := m.db.ExecContext(ctx,
		`DELETE FROM skill_overrides WHERE skill_name = ? AND scope = 'user' AND user_id = ?`, name, userID)
	return err
}
