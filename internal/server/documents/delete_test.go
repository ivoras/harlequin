package documents

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/ivoras/harlequin/internal/server/db"
)

// Deleting a document must remove its chunks, FTS and vector rows and the
// stored file, and leave other documents (and their files) alone.
func TestDeleteFromRemovesRowsIndexesAndFile(t *testing.T) {
	dir := t.TempDir()
	const dim = 4
	pdb, err := db.Open(filepath.Join(dir, "project.db"), db.Project, dim)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	files := filepath.Join(dir, "files")
	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	blob, err := sqlite_vec.SerializeFloat32([]float32{1, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, stored string) int64 {
		res, err := pdb.Exec(`INSERT INTO documents(title, stored_path) VALUES (?, ?)`, title, stored)
		if err != nil {
			t.Fatal(err)
		}
		docID, _ := res.LastInsertId()
		res, err = pdb.Exec(`INSERT INTO doc_chunks(document_id, ord, content) VALUES (?, 0, ?)`, docID, title)
		if err != nil {
			t.Fatal(err)
		}
		cid, _ := res.LastInsertId()
		if _, err := pdb.Exec(`INSERT INTO doc_chunks_fts(rowid, content) VALUES (?, ?)`, cid, title); err != nil {
			t.Fatal(err)
		}
		if _, err := pdb.Exec(`INSERT INTO doc_chunks_vec(rowid, embedding) VALUES (?, ?)`, cid, blob); err != nil {
			t.Fatal(err)
		}
		if stored != "" {
			if err := os.WriteFile(filepath.Join(files, stored), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return docID
	}
	gone := add("gone", "1-gone.pdf")
	kept := add("kept", "2-kept.pdf")

	s := &Store{}
	if err := s.DeleteFrom(ctx, pdb, files, gone); err != nil {
		t.Fatalf("DeleteFrom: %v", err)
	}

	count := func(q string) int {
		var n int
		if err := pdb.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, tbl := range []string{"documents", "doc_chunks", "doc_chunks_fts", "doc_chunks_vec"} {
		if n := count(`SELECT COUNT(*) FROM ` + tbl); n != 1 {
			t.Errorf("%s has %d rows after delete, want 1 (the kept doc)", tbl, n)
		}
	}
	if _, err := os.Stat(filepath.Join(files, "1-gone.pdf")); !os.IsNotExist(err) {
		t.Errorf("stored file of deleted doc still present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(files, "2-kept.pdf")); err != nil {
		t.Errorf("other doc's file was removed: %v", err)
	}

	// A document without a stored file, or one whose file is already gone, deletes cleanly.
	if err := s.DeleteFrom(ctx, pdb, files, kept); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(files, "2-kept.pdf"))
	if err := s.DeleteFrom(ctx, pdb, files, 9999); err != nil {
		t.Fatalf("deleting a missing doc: %v", err)
	}
}
