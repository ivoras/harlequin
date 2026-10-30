package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteProjectData(t *testing.T) {
	data := t.TempDir()
	m := &Manager{dataDir: data}
	keep := filepath.Join(ProjectDir(data, 2), "files")
	gone := filepath.Join(ProjectDir(data, 1), "files")
	for _, d := range []string{keep, gone} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DeleteProjectData(1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ProjectDir(data, 1)); !os.IsNotExist(err) {
		t.Errorf("project dir still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(keep, "f")); err != nil {
		t.Errorf("other project's data was touched: %v", err)
	}
	if err := m.DeleteProjectData(0); err == nil {
		t.Error("project id 0 must be rejected (would target the projects/ root)")
	}
}
