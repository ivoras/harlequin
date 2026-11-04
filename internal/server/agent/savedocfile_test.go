package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
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

// TestSaveDocIsRawSkipsCitationCheck covers the is_raw flag: original writing
// (e.g. a story) whose dialogue contains a long quoted sentence would otherwise
// be rejected by the quoted-citation check as an uncited corpus excerpt. With
// is_raw=true that check is skipped and the same content saves; without it, the
// content is rejected. Exercised end-to-end through the save_doc tool handler.
func TestSaveDocIsRawSkipsCitationCheck(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", db.Shared, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	a := &Agent{Docs: documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4})}
	rc := &runContext{userDB: sqlDB, userID: 1}
	save := a.buildTools(ctx, rc)["save_doc"].handler

	// A story whose dialogue is a long verbatim quotation with no citation —
	// exactly what the citation verifier flags as an uncited excerpt.
	content := "# Ludwig Returns\n\nBarnaby cleared his throat. \"Good morning to you all, and welcome back to the meadow where our very first adventure began\" he announced to the gathered woodchucks."

	// Without is_raw the citation check rejects it.
	out, err := save(ctx, rc, map[string]any{"title": "Ludwig Returns", "content": content, "scope": "personal"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not saved") {
		t.Fatalf("expected the citation check to reject the story, got: %q", out)
	}

	// With is_raw=true the same content saves.
	out, err = save(ctx, rc, map[string]any{"title": "Ludwig Returns", "content": content, "scope": "personal", "is_raw": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Saved as document") {
		t.Fatalf("is_raw should bypass the citation check and save, got: %q", out)
	}
}

// TestDeleteDocTool covers the delete_doc handler: a personal document is
// resolved by its scoped id and removed from the corpus (row and its persisted
// on-disk file), and a shared-scope delete is refused for a non-elevated user.
func TestDeleteDocTool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sqlDB := mgr.Shared

	store := documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4})
	a := &Agent{Docs: store, Storage: mgr}
	rc := &runContext{userDB: sqlDB, userID: 1}
	del := a.buildTools(ctx, rc)["delete_doc"].handler

	doc, err := store.IngestInto(ctx, sqlDB, types.CreateDocumentRequest{
		Title: "Disposable", URI: "agent://save_doc", Mime: "text/plain", Content: "Some text to store then delete.",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Persist an on-disk file for the doc (as save_doc/uploads do), so we can
	// assert delete_doc unlinks it. Personal docs live in the user files dir.
	content := "Some text to store then delete."
	a.storeSavedDocFile(ctx, sqlDB, documents.ScopePersonal, rc, doc, content)
	storedPath, _, _, err := store.StoredFile(ctx, sqlDB, doc.ID)
	if err != nil || storedPath == "" {
		t.Fatalf("expected a stored file to exist before delete (path=%q, err=%v)", storedPath, err)
	}
	userFiles, _ := mgr.UserFilesDir(rc.userID)
	fullPath := filepath.Join(userFiles, storedPath)
	if _, err := os.Stat(fullPath); err != nil {
		t.Fatalf("stored file should exist before delete: %v", err)
	}

	out, err := del(ctx, rc, map[string]any{"doc": "u." + strconv.FormatInt(doc.ID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Deleted document") {
		t.Fatalf("expected a delete confirmation, got: %q", out)
	}
	var n int
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM documents WHERE id = ?", doc.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("document row should be gone after delete, found %d", n)
	}
	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		t.Fatalf("stored file should be unlinked after delete, stat err = %v", err)
	}

	// A shared-scope delete is refused when the user is not owner/admin. Ingest
	// into the shared corpus (rc has canShareMemory=false).
	sdoc, err := store.IngestInto(ctx, store.SharedDB(), types.CreateDocumentRequest{
		Title: "Org policy", URI: "agent://save_doc", Mime: "text/plain", Content: "A shared document.",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err = del(ctx, rc, map[string]any{"doc": "s." + strconv.FormatInt(sdoc.ID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "owner or admin") {
		t.Fatalf("shared delete should be refused for a non-elevated user, got: %q", out)
	}
}

// TestSaveDocFromSource covers save_doc's source parameter: a sandbox file is
// read server-side and saved verbatim; content+source together, a missing file,
// and non-UTF-8 data are rejected.
func TestSaveDocFromSource(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", db.Shared, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()

	a := &Agent{DataDir: t.TempDir(), Docs: documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4})}
	rc := &runContext{userDB: sqlDB, userID: 1}
	save := a.buildTools(ctx, rc)["save_doc"].handler

	combined := "# Combined\n\nČlanak 1. Zakon.\n\nČlanak 2. Izmjena."
	if err := a.tmpRoot(1).Write("combined.md", []byte(combined)); err != nil {
		t.Fatal(err)
	}
	if err := a.tmpRoot(1).Write("bin.dat", []byte{0xff, 0xfe, 0x00, 0x01}); err != nil {
		t.Fatal(err)
	}

	out, _ := save(ctx, rc, map[string]any{"title": "Combined", "source": "tmp://combined.md", "scope": "personal", "is_raw": true})
	if !strings.Contains(out, "Saved as document") {
		t.Fatalf("source save failed: %q", out)
	}
	docs, err := a.Docs.List(ctx)
	if err != nil || len(docs) != 1 {
		t.Fatalf("expected one saved document, got %d (err %v)", len(docs), err)
	}

	for name, args := range map[string]map[string]any{
		"both":     {"title": "x", "source": "tmp://combined.md", "content": "inline", "is_raw": true},
		"missing":  {"title": "x", "source": "tmp://nope.md", "is_raw": true},
		"binary":   {"title": "x", "source": "tmp://bin.dat", "is_raw": true},
		"badproto": {"title": "x", "source": "http://example.com/a.md", "is_raw": true},
		"neither":  {"title": "x", "is_raw": true},
	} {
		out, _ := save(ctx, rc, args)
		if !strings.HasPrefix(out, "error:") {
			t.Errorf("%s: expected an error, got %q", name, out)
		}
	}
}

// Soft hyphens (U+00AD) in a saved document are stripped from the index and the
// stored original, so lexical search finds the whole word.
func TestSaveDocStripsSoftHyphens(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sqlDB := mgr.Shared
	a := &Agent{DataDir: dir, Docs: documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4}), Storage: mgr}
	rc := &runContext{userDB: sqlDB, userID: 1}
	save := a.buildTools(ctx, rc)["save_doc"].handler

	if err := a.tmpRoot(1).Write("s.md", []byte("Hrvat­ski sabor donio je zakon o obveznim od­nosima.")); err != nil {
		t.Fatal(err)
	}
	out, _ := save(ctx, rc, map[string]any{"title": "Soft", "source": "tmp://s.md", "scope": "personal", "is_raw": true})
	if !strings.Contains(out, "Saved as document") {
		t.Fatalf("save failed: %q", out)
	}
	docs, _ := a.Docs.List(ctx)
	text, _ := a.Docs.FullText(ctx, sqlDB, docs[0].ID)
	if strings.Contains(text, "­") || !strings.Contains(text, "Hrvatski sabor") {
		t.Errorf("index text still has soft hyphens: %q", text)
	}
	stored, _, _, _ := a.Docs.StoredFile(ctx, sqlDB, docs[0].ID)
	files, _ := mgr.UserFilesDir(1)
	raw, _ := os.ReadFile(filepath.Join(files, stored))
	if strings.Contains(string(raw), "­") {
		t.Errorf("stored original still has soft hyphens: %q", raw)
	}
	var n int
	sqlDB.QueryRow(`SELECT COUNT(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH 'Hrvatski'`).Scan(&n)
	if n != 1 {
		t.Errorf("FTS should find 'Hrvatski', got %d hits", n)
	}
}
