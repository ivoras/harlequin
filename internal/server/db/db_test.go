package db

import (
	"path/filepath"
	"testing"
)

func TestFileDatabasesUseWAL(t *testing.T) {
	for _, role := range []Role{System, Shared, User, Project} {
		d, err := Open(filepath.Join(t.TempDir(), "x.db"), role, 4)
		if err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Errorf("role %v: journal_mode = %q, %v", role, mode, err)
		}
		d.Close()
	}
}
