package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mdpdf"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// create_pdf stores a real PDF as the downloadable original with the Markdown
// as the searchable text; without a renderer it says so instead of vanishing.
func TestCreatePDFTool(t *testing.T) {
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
	create := a.buildTools(ctx, rc)["create_pdf"].handler
	md := "# Izvještaj\n\nPrva.\n\n\\newpage\n\n| A | B |\n|---|---:|\n| Đuro | 1,50 |\n\n![logo](http://example.com/l.png)\n"

	if out, _ := create(ctx, rc, map[string]any{"title": "x", "content": md}); !strings.Contains(out, "not set up on this server") {
		t.Fatalf("without a renderer: %q", out)
	}
	if a.PDF, err = mdpdf.New(""); err != nil {
		t.Skip(err)
	}
	out, err := create(ctx, rc, map[string]any{"title": "Plaće 10", "content": md})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"Plaće 10.pdf": 2 page(s)`) || !strings.Contains(out, "1 image(s) were left out") {
		t.Fatalf("unexpected result: %q", out)
	}
	var id int64
	var mime string
	if err := db.QueryRow(`SELECT id, mime FROM documents WHERE title = 'Plaće 10.pdf'`).Scan(&id, &mime); err != nil {
		t.Fatal(err)
	}
	stored, _, _, err := store.StoredFile(ctx, db, id)
	if err != nil || mime != "application/pdf" || !strings.HasSuffix(stored, ".pdf") || strings.HasSuffix(stored, ".pdf.pdf") {
		t.Fatalf("mime %q, stored path %q (%v)", mime, stored, err)
	}
	files, _ := mgr.UserFilesDir(1)
	if data, err := os.ReadFile(filepath.Join(files, stored)); err != nil || !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("stored file is not a PDF (%v)", err)
	}
	if text, _ := store.FullText(ctx, db, id); !strings.Contains(text, "| Đuro | 1,50 |") {
		t.Fatalf("searchable text: %q", text)
	}

	// HTML input (explicit, or recognised from the file name / the text), and a
	// saved document as the source.
	html := `<style>h1{color:#c00}</style><h1>Dopis</h1><p onclick="x()">Poštovani,</p><script>alert(1)</script><img src="http://example.com/l.png">`
	if err := a.tmpRoot(1).Write("dopis.html", []byte(html)); err != nil {
		t.Fatal(err)
	}
	for title, args := range map[string]map[string]any{
		"h1": {"content": html, "format": "html"},
		"h2": {"source": "tmp://dopis.html"},
		"h3": {"content": "<!DOCTYPE html><html><body>" + html + "</body></html>"},
	} {
		args["title"] = title
		out, _ := create(ctx, rc, args)
		if !strings.Contains(out, "1 page(s)") || !strings.Contains(out, "1 image(s) were left out") || !strings.Contains(out, "2 script, form") {
			t.Errorf("%s: %q", title, out)
		}
		var tid int64
		_ = db.QueryRow(`SELECT id FROM documents WHERE title = ?`, title+".pdf").Scan(&tid)
		if text, _ := store.FullText(ctx, db, tid); !strings.Contains(text, "# Dopis") || strings.Contains(text, "<h1") || strings.Contains(text, "alert") {
			t.Errorf("%s searchable text: %q", title, text)
		}
	}
	if out, _ := create(ctx, rc, map[string]any{"title": "from doc", "source": "s." + strconv.FormatInt(id, 10)}); !strings.Contains(out, `"from doc.pdf": 2 page(s)`) {
		t.Errorf("document as source: %q", out)
	}
	// Markdown that merely mentions HTML stays Markdown.
	if out, _ := create(ctx, rc, map[string]any{"title": "md", "content": "Tekst o <html> oznakama"}); !strings.Contains(out, "1 page(s)") || strings.Contains(out, "removed") {
		t.Errorf("markdown: %q", out)
	}

	for args, want := range map[*map[string]any]string{
		{"title": "x", "content": md, "format": "docx"}:                   "format must be",
		{"title": "x", "content": "<script>x</script>", "format": "html"}: "no visible text",
		{"title": "x", "content": "  "}:                                   "empty",
		{"title": "", "content": md}:                                      "title is required",
		{"title": "x", "content": md, "source": "tmp://a"}:                "not both",
		{"title": "x", "content": md, "scope": "shared"}:                  "owner or admin",
		{"title": "x", "content": md, "scope": "project"}:                 "project session",
		{"title": "x", "source": "tmp://does-not-exist.md"}:               "error:",
	} {
		if out, _ := create(ctx, rc, *args); !strings.Contains(out, want) {
			t.Errorf("%v: got %q, want %q", *args, out, want)
		}
	}
}
