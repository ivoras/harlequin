package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepTmp(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{DataDir: dir}

	// Two users, each with an old and a fresh tmp file.
	for _, uid := range []int64{3, 7} {
		tmp := a.userTmpDir(uid)
		if err := os.MkdirAll(tmp, 0o755); err != nil {
			t.Fatal(err)
		}
		old := filepath.Join(tmp, "old.html")
		fresh := filepath.Join(tmp, "fresh.html")
		for _, f := range []string{old, fresh} {
			if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// Backdate the old file well past the retention window.
		past := time.Now().AddDate(0, 0, -8)
		if err := os.Chtimes(old, past, past); err != nil {
			t.Fatal(err)
		}
	}

	a.SweepTmp(7)

	for _, uid := range []int64{3, 7} {
		tmp := a.userTmpDir(uid)
		if _, err := os.Stat(filepath.Join(tmp, "old.html")); !os.IsNotExist(err) {
			t.Errorf("user %d: old.html should have been swept, err=%v", uid, err)
		}
		if _, err := os.Stat(filepath.Join(tmp, "fresh.html")); err != nil {
			t.Errorf("user %d: fresh.html should remain, err=%v", uid, err)
		}
	}

	// retentionDays <= 0 disables the sweep (and must not panic on a fresh tree).
	a.SweepTmp(0)
	(&Agent{DataDir: ""}).SweepTmp(7) // empty DataDir is a no-op
}
