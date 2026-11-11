package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mdword"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// create_word stores a real DOCX as the downloadable original with the
// Markdown as the searchable text; without a renderer it says so.
func TestCreateWordTool(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	db := mgr.Shared
	store := documents.NewStore(db, crossCheckFakeEmbedder{dim: 4})
	a := &Agent{DataDir: dir, Docs: store, Storage: mgr}
	rc := &runContext{userDB: db, userID: 1}
	create := a.buildTools(ctx, rc)["create_word"].handler
	md := "# Izvještaj\n\nPrva.\n\n\\newpage\n\n| A | B |\n|---|---:|\n| Đuro | 1,50 |\n\n![logo](http://example.com/l.png)\n"

	if out, _ := create(ctx, rc, map[string]any{"title": "x", "content": md}); !strings.Contains(out, "not set up on this server") {
		t.Fatalf("without a renderer: %q", out)
	}
	if a.Word, err = mdword.New(""); err != nil {
		t.Skip(err)
	}
	out, err := create(ctx, rc, map[string]any{"title": "Plaće 10", "content": md})
	if err != nil || !strings.Contains(out, `"Plaće 10.docx"`) || !strings.Contains(out, "1 image(s) were left out") {
		t.Fatalf("unexpected result: %q (%v)", out, err)
	}
	var id int64
	var mime string
	if err := db.QueryRow(`SELECT id, mime FROM documents WHERE title = 'Plaće 10.docx'`).Scan(&id, &mime); err != nil {
		t.Fatal(err)
	}
	stored, _, _, err := store.StoredFile(ctx, db, id)
	if err != nil || mime != docxMime || !strings.HasSuffix(stored, ".docx") || strings.HasSuffix(stored, ".docx.docx") {
		t.Fatalf("mime %q, stored path %q (%v)", mime, stored, err)
	}
	files, _ := mgr.UserFilesDir(1)
	if data, err := os.ReadFile(filepath.Join(files, stored)); err != nil || !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("stored file is not a DOCX (%v)", err)
	}
	if text, _ := store.FullText(ctx, db, id); !strings.Contains(text, "| Đuro | 1,50 |") {
		t.Fatalf("searchable text: %q", text)
	}
	// HTML input is given as format, and its active content reported.
	out, _ = create(ctx, rc, map[string]any{"title": "h", "content": `<h1>Dopis</h1><script>alert(1)</script>`, "format": "html"})
	if !strings.Contains(out, "1 script, form") {
		t.Errorf("html: %q", out)
	}
}
