package documents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// CopyInto reproduces a document in another corpus — text, chunks, FTS and
// vector rows, stored file — so it's searchable there at once; the source stays.
func TestCopyIntoAnotherCorpus(t *testing.T) {
	ctx := context.Background()
	src, err := db.Open(":memory:", db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := db.Open(":memory:", db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	s := NewStore(nil, fakeEmbedder{dim: 4})
	doc, err := s.IngestInto(ctx, src, types.CreateDocumentRequest{
		Title: "Contract.pdf", Mime: "application/pdf",
		Content: "The tenant pays the rent monthly. " + strings.Repeat("Further obligations follow. ", 80),
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	srcFiles, dstFiles := t.TempDir(), t.TempDir()
	stored := "12-Contract.pdf"
	os.WriteFile(filepath.Join(srcFiles, stored), []byte("%PDF original"), 0o600)
	src.Exec(`UPDATE documents SET stored_path = ?, description = 'a lease' WHERE id = ?`, stored, doc.ID)
	// Make the target's ids differ from the source's.
	dst.Exec(`INSERT INTO documents(title) VALUES ('other')`)

	newID, err := s.CopyInto(ctx, src, dst, srcFiles, dstFiles, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if newID == doc.ID {
		t.Fatalf("expected a fresh id in the target, got %d", newID)
	}
	var title, desc, path string
	var by int64
	dst.QueryRow(`SELECT title, description, stored_path, created_by FROM documents WHERE id = ?`, newID).Scan(&title, &desc, &path, &by)
	if title != "Contract.pdf" || desc != "a lease" || by != 7 || path != filepath.Base(path) || !strings.HasPrefix(path, "2-") {
		t.Fatalf("copied row: %q %q %q %d", title, desc, path, by)
	}
	if b, _ := os.ReadFile(filepath.Join(dstFiles, path)); string(b) != "%PDF original" {
		t.Fatalf("stored file not copied: %q", b)
	}
	var nSrc, nDst, nVec, nFTS int
	src.QueryRow(`SELECT COUNT(*) FROM doc_chunks WHERE document_id = ?`, doc.ID).Scan(&nSrc)
	dst.QueryRow(`SELECT COUNT(*) FROM doc_chunks WHERE document_id = ?`, newID).Scan(&nDst)
	dst.QueryRow(`SELECT COUNT(*) FROM doc_chunks_vec`).Scan(&nVec)
	dst.QueryRow(`SELECT COUNT(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH 'tenant'`).Scan(&nFTS)
	if nSrc == 0 || nDst != nSrc || nVec != nSrc || nFTS != 1 {
		t.Fatalf("chunks src=%d dst=%d vec=%d fts(tenant)=%d", nSrc, nDst, nVec, nFTS)
	}
	if text, _ := s.FullText(ctx, dst, newID); !strings.Contains(text, "The tenant pays the rent monthly.") {
		t.Fatalf("full text: %q", text[:min(len(text), 80)])
	}
}
