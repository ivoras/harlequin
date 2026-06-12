// Package storage manages Harlequin's three-tier SQLite layout: a kept-open
// system database (users/auth), a kept-open shared/org database, and per-user
// databases that are opened and closed around each request.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/ivoras/harlequin/internal/server/db"
)

// Manager owns the database handles and per-user database lifecycle.
type Manager struct {
	System *sql.DB // harlequin.db, kept open
	Shared *sql.DB // shared.db, kept open

	dataDir string
	dim     int
	inited  sync.Map // userID -> struct{}: user dbs whose migrations have run this process
}

// New opens the system and shared databases (kept open for the process
// lifetime) and returns a Manager. systemPath lets the caller honor a
// HARLEQUIN_DB_PATH override for the system database; the shared database always
// lives at <dataDir>/shared.db.
func New(dataDir, systemPath string, dim int) (*Manager, error) {
	system, err := db.Open(systemPath, db.System, dim)
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}
	shared, err := db.Open(filepath.Join(dataDir, "shared.db"), db.Shared, dim)
	if err != nil {
		system.Close()
		return nil, fmt.Errorf("open shared db: %w", err)
	}
	return &Manager{System: system, Shared: shared, dataDir: dataDir, dim: dim}, nil
}

// Close closes the kept-open databases.
func (m *Manager) Close() error {
	var first error
	if err := m.Shared.Close(); err != nil {
		first = err
	}
	if err := m.System.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// UserDir returns the directory holding a user's database and files under dataDir.
func UserDir(dataDir string, userID int64) string {
	return filepath.Join(dataDir, "users", strconv.FormatInt(userID, 10))
}

// UserDir returns the directory holding a user's database and files.
func (m *Manager) UserDir(userID int64) string {
	return UserDir(m.dataDir, userID)
}

// UserDBPath returns the path of a user's database file.
func (m *Manager) UserDBPath(userID int64) string {
	return filepath.Join(m.UserDir(userID), "user.db")
}

// UserFilesDir returns (and creates) the directory for a user's uploaded files.
func (m *Manager) UserFilesDir(userID int64) (string, error) {
	dir := filepath.Join(m.UserDir(userID), "files")
	return dir, os.MkdirAll(dir, 0o755)
}

// SharedFilesDir returns (and creates) the directory for uploaded shared files.
func (m *Manager) SharedFilesDir() (string, error) {
	dir := filepath.Join(m.dataDir, "shared_files")
	return dir, os.MkdirAll(dir, 0o755)
}

// openUser opens a user's database. The first open per process runs migrations
// and creates virtual tables; subsequent opens skip that schema work (tracked by
// inited), so the per-request open/close stays cheap. The caller is responsible
// for closing the returned handle.
func (m *Manager) openUser(userID int64) (*sql.DB, error) {
	path := m.UserDBPath(userID)
	if _, ok := m.inited.Load(userID); ok {
		udb, err := db.OpenInitialized(path)
		if err != nil {
			return nil, fmt.Errorf("open user db %d: %w", userID, err)
		}
		return udb, nil
	}
	udb, err := db.Open(path, db.User, m.dim)
	if err != nil {
		return nil, fmt.Errorf("open user db %d: %w", userID, err)
	}
	m.inited.Store(userID, struct{}{})
	return udb, nil
}

// WithUser opens the user's database, runs fn with it, then closes it. Per the
// architecture, per-user databases are not kept open across requests.
func (m *Manager) WithUser(ctx context.Context, userID int64, fn func(userDB *sql.DB) error) error {
	udb, err := m.openUser(userID)
	if err != nil {
		return err
	}
	defer udb.Close()
	return fn(udb)
}

// WithUserReadOnly opens the user's database read-only, runs fn with it, then
// closes it. A read-only connection never checkpoints the WAL on close, so this
// is the cheap, low-variance path for pure readers on hot endpoints. Because a
// read-only connection cannot run migrations, the first call per process for a
// user does a one-time read-write open to ensure the schema is present.
func (m *Manager) WithUserReadOnly(ctx context.Context, userID int64, fn func(userDB *sql.DB) error) error {
	if _, ok := m.inited.Load(userID); !ok {
		// Ensure the schema exists/migrated once before opening read-only.
		if err := m.WithUser(ctx, userID, func(*sql.DB) error { return nil }); err != nil {
			return err
		}
	}
	udb, err := db.OpenReadOnly(m.UserDBPath(userID))
	if err != nil {
		return err
	}
	defer udb.Close()
	return fn(udb)
}

// EachUser opens every user's database in turn (newest user ids first is not
// guaranteed) and invokes fn. Used for cross-user maintenance and admin
// aggregation. Errors from fn stop the iteration.
func (m *Manager) EachUser(ctx context.Context, fn func(userID int64, userDB *sql.DB) error) error {
	rows, err := m.System.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.WithUser(ctx, id, func(udb *sql.DB) error {
			return fn(id, udb)
		}); err != nil {
			return err
		}
	}
	return nil
}
