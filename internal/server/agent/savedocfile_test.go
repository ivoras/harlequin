package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// TestStoreSavedDocFilePersistsExactContent verifies save_doc's follow-up
// disk write: a report ingested via save_doc previously had no on-disk
// representation at all, only its chunked, whitespace-normalized form —
// meaning /file couldn't serve it and the reconstruct-from-chunks fallback
// (FullText) could visibly mangle content that happened to split awkwardly
// at a chunk boundary. storeSavedDocFile writes the exact original bytes to
// the scope's files/ directory and records stored_path, exactly as PDF/DOCX
// uploads do (handleCreateDocument's ingestAndStore), so the original is
// always available verbatim.
func TestStoreSavedDocFilePersistsExactContent(t *testing.T) {
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	a := &Agent{Storage: mgr, Docs: documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})}
	ctx := context.Background()
	content := "Line one [d.p. 3651] continues.\nLine two."
	doc, err := a.Docs.IngestInto(ctx, mgr.Shared, types.CreateDocumentRequest{
		Title: "Weird/Title: Report", URI: "agent://save_doc", Mime: "text/plain", Content: content,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	rc := &runContext{userID: 1}
	a.storeSavedDocFile(ctx, mgr.Shared, documents.ScopeShared, rc, doc, content)

	storedPath, _, _, err := a.Docs.StoredFile(ctx, mgr.Shared, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedPath == "" {
		t.Fatal("stored_path was not recorded")
	}
	filesDir, err := mgr.SharedFilesDir()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(filesDir, storedPath))
	if err != nil {
		t.Fatalf("reading persisted file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("persisted file content = %q, want %q", got, content)
	}
	if strings.ContainsAny(storedPath, "/: ") {
		t.Fatalf("stored filename %q should be filesystem-safe (ascii, no separators)", storedPath)
	}
}
