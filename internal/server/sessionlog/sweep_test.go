package sessionlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepRetention(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "00001.00002.jsonl")
	newPath := filepath.Join(dir, "00001.00003.jsonl")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}

	l := New(dir, false, false, nil)
	l.SweepRetention(7)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatal("expected old session file removed")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("expected recent session file kept: %v", err)
	}
}

func TestSweepRetentionZeroKeepsAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "00001.00002.jsonl")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	l := New(dir, false, false, nil)
	l.SweepRetention(0)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("retention 0 should not delete: %v", err)
	}
}
