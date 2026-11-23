package documents

import (
	"context"
	"testing"

	"github.com/ivoras/harlequin/internal/shared/types"
)

// A big spreadsheet gets vectors for its first MaxSheetEmbedChunks chunks only;
// every chunk stays in FTS. Other documents are embedded whole.
func TestSheetEmbedCap(t *testing.T) {
	ctx := context.Background()
	s, e, d := replSetup(t)
	content := filler("row", 1600*(MaxSheetEmbedChunks+100))
	doc, err := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "big.xlsx", Mime: "text/csv", Content: content}, 1)
	if err != nil {
		t.Fatal(err)
	}
	var chunks, fts, vecs int
	_ = d.QueryRow(`SELECT count(*) FROM doc_chunks WHERE document_id = ?`, doc.ID).Scan(&chunks)
	_ = d.QueryRow(`SELECT count(*) FROM doc_chunks_fts`).Scan(&fts)
	_ = d.QueryRow(`SELECT count(*) FROM doc_chunks_vec`).Scan(&vecs)
	if chunks <= MaxSheetEmbedChunks || fts != chunks || vecs != MaxSheetEmbedChunks || e.texts != MaxSheetEmbedChunks {
		t.Fatalf("chunks %d, fts %d, vectors %d, embedded %d", chunks, fts, vecs, e.texts)
	}
	// Keyword search still finds a row past the cap.
	if hits, err := s.FindText(ctx, d, doc.ID, "row70000", 1); err != nil || len(hits) == 0 {
		t.Fatalf("late row not found: %v %v", hits, err)
	}
	e.texts = 0
	if _, err := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "big.md", Mime: "text/markdown", Content: content}, 1); err != nil {
		t.Fatal(err)
	}
	if e.texts != chunks {
		t.Fatalf("text document: embedded %d of %d chunks", e.texts, chunks)
	}
}
