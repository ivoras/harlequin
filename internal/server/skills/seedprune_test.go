package skills

import (
	"context"
	"database/sql"
	"testing"
	"testing/fstest"

	_ "github.com/mattn/go-sqlite3"
)

func openHatDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE hats (name TEXT PRIMARY KEY, description TEXT NOT NULL DEFAULT '', updated_by INTEGER, updated_at TIMESTAMP);
		CREATE TABLE hat_files (hat_name TEXT NOT NULL REFERENCES hats(name) ON DELETE CASCADE, relpath TEXT NOT NULL, content BLOB NOT NULL, PRIMARY KEY(hat_name, relpath));
		CREATE TABLE hat_seed_hashes (relpath TEXT PRIMARY KEY, hash TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	return db
}

// A hat dropped from the baked tree is deleted if untouched and kept if edited.
func TestSeedPrunesRemovedHats(t *testing.T) {
	ctx := context.Background()
	db := openHatDB(t)
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	both := fstest.MapFS{
		"hats/keep/system_prompt.md": file("---\ndescription: k\n---\n"),
		"hats/old/system_prompt.md":  file("---\ndescription: o\n---\n"),
		"hats/edit/system_prompt.md": file("---\ndescription: e\n---\n"),
	}
	if err := SeedHats(ctx, db, both, "hats"); err != nil {
		t.Fatal(err)
	}
	if err := writeItemFile(ctx, db, hatTables, "edit", hatPromptFile, "---\ndescription: mine\n---\n"); err != nil {
		t.Fatal(err)
	}
	if err := SeedHats(ctx, db, fstest.MapFS{"hats/keep/system_prompt.md": both["hats/keep/system_prompt.md"]}, "hats"); err != nil {
		t.Fatal(err)
	}
	var n int
	for name, want := range map[string]int{"keep": 1, "old": 0, "edit": 1} {
		db.QueryRow(`SELECT COUNT(*) FROM hats WHERE name = ?`, name).Scan(&n)
		if n != want {
			t.Errorf("hat %s: count %d, want %d", name, n, want)
		}
	}
	db.QueryRow(`SELECT COUNT(*) FROM hat_files WHERE hat_name = 'old'`).Scan(&n)
	if n != 0 {
		t.Error("files of the removed hat remain")
	}
}
