// Package db opens Harlequin SQLite databases (CGO, WAL, foreign keys),
// registers the sqlite-vec extension, runs embedded migrations for the database
// role, and creates the dimension-dependent FTS5 and vec0 virtual tables.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/system/*.sql migrations/shared/*.sql migrations/user/*.sql
var migrationsFS embed.FS

var registerOnce sync.Once

// Role selects which schema a database file carries.
type Role string

const (
	// System is the main harlequin.db: users, auth, API tokens.
	System Role = "system"
	// Shared is the org-level shared.db: shared memories, documents, org skills.
	Shared Role = "shared"
	// User is a per-user user.db: that user's memories, conversations, etc.
	User Role = "user"
)

// openConn opens a WAL connection to the database file (registering sqlite-vec
// and creating the parent directory) without touching the schema.
func openConn(path string) (*sql.DB, error) {
	// Register sqlite-vec so vec0 is available on every connection.
	registerOnce.Do(func() {
		sqlite_vec.Auto()
	})

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	dsn := path + "?_journal=WAL&_foreign_keys=on&_busy_timeout=5000"
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// sqlite-vec / writes are simplest with a single connection for the embedded DB.
	sqlDB.SetMaxOpenConns(1)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return sqlDB, nil
}

// Open opens (and initializes) the database at path for the given role, with
// vector dimension dim for embedding columns. It runs the role's migrations and
// creates the role's virtual tables.
func Open(path string, role Role, dim int) (*sql.DB, error) {
	sqlDB, err := openConn(path)
	if err != nil {
		return nil, err
	}

	if err := runMigrations(sqlDB, role); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	if err := createVirtualTables(sqlDB, role, dim); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("create virtual tables: %w", err)
	}

	return sqlDB, nil
}

// OpenInitialized opens a database whose schema is already present — its
// migrations and virtual tables were created by a prior Open in this process.
// It skips all schema work, making repeated per-request opens cheap.
func OpenInitialized(path string) (*sql.DB, error) {
	return openConn(path)
}

func runMigrations(sqlDB *sql.DB, role Role) error {
	if _, err := sqlDB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}

	dir := "migrations/" + string(role)
	entries, err := migrationsFS.ReadDir(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		raw, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		if _, err := sqlDB.Exec(string(raw)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := sqlDB.Exec(`INSERT INTO schema_migrations(name) VALUES (?)`, name); err != nil {
			return err
		}
	}
	return nil
}

// createVirtualTables creates the FTS5 and vec0 tables a role needs. These
// depend on the embedding dimension, so they are created in Go rather than SQL.
// The system database has no searchable corpus and gets none.
func createVirtualTables(sqlDB *sql.DB, role Role, dim int) error {
	var stmts []string
	switch role {
	case Shared:
		stmts = []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(content)`,
			fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS memories_vec USING vec0(embedding float[%d])`, dim),
			fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_slots_vec USING vec0(embedding float[%d])`, dim),
			`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunks_fts USING fts5(content)`,
			fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunks_vec USING vec0(embedding float[%d])`, dim),
		}
	case User:
		stmts = []string{
			`CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(content)`,
			fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS memories_vec USING vec0(embedding float[%d])`, dim),
			fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_slots_vec USING vec0(embedding float[%d])`, dim),
		}
	case System:
		return nil
	}
	for _, s := range stmts {
		if _, err := sqlDB.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}
	return nil
}
