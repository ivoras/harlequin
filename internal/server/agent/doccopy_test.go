package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// The edit tools' append_source and copy_doc move whole texts between saved documents and sandbox
// files without the text passing through the model.
func TestAppendAndCopyDocTools(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	// A real per-user database, so the documents are personal (u.N).
	if err := mgr.WithUser(ctx, 1, func(db *sql.DB) error {
		testAppendAndCopy(t, ctx, dir, mgr, db)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func testAppendAndCopy(t *testing.T, ctx context.Context, dir string, mgr *storage.Manager, db *sql.DB) {
	store := documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})
	a := &Agent{DataDir: dir, Docs: store, Storage: mgr}
	rc := &runContext{userDB: db, userID: 1}
	tools := a.buildTools(ctx, rc)
	call := func(tool string, args map[string]any) string {
		t.Helper()
		out, err := tools[tool].handler(ctx, rc, args)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return out
	}
	ref := func(title string) string {
		t.Helper()
		var id int64
		if err := db.QueryRow(`SELECT id FROM documents WHERE title = ? ORDER BY id DESC`, title).Scan(&id); err != nil {
			t.Fatalf("document %q: %v", title, err)
		}
		return "u." + strconv.FormatInt(id, 10)
	}
	stored := func(r string) string {
		t.Helper()
		id, _ := strconv.ParseInt(strings.TrimPrefix(r, "u."), 10, 64)
		name, _, _, _ := store.StoredFile(ctx, db, id)
		data, err := os.ReadFile(filepath.Join(a.docFilesDir(rc, documents.ScopePersonal), name))
		if err != nil {
			t.Fatalf("stored file of %s: %v", r, err)
		}
		return string(data)
	}

	// Two saved text documents (as save_doc makes them) and an uploaded PDF,
	// which has text in the index but no text original.
	call("save_doc", map[string]any{"title": "Prvi", "content": "# Prvi\n\n| A | B |\n|---|---|\n| 1 | 2 |", "is_raw": true})
	call("save_doc", map[string]any{"title": "Drugi", "content": "# Drugi\n\nTekst drugog.", "is_raw": true})
	first, second := ref("Prvi"), ref("Drugi")
	pdfDoc, err := store.IngestInto(ctx, db, types.CreateDocumentRequest{Title: "sken.pdf", Mime: "application/pdf", Content: "# Sken\n\nTekst iz PDF-a."}, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.storeDocFile(ctx, db, documents.ScopePersonal, rc, pdfDoc, []byte("%PDF-1.4 fake"), ".pdf")
	pdf := "u." + strconv.FormatInt(pdfDoc.ID, 10)

	// Concatenate into a new sandbox file, then onto it again (a single string
	// is accepted where the list is declared; a bare path means tmp://).
	out := call("edit_file", map[string]any{"path": "tmp://sve.md", "append_source": []any{first, second}})
	if !strings.Contains(out, `from document `+first+` ("Prvi"), document `+second+` ("Drugi")`) {
		t.Fatalf("append to new file: %q", out)
	}
	call("edit_file", map[string]any{"path": "sve.md", "append_source": pdf})
	data, _ := a.tmpRoot(1).Read("sve.md")
	if got, want := string(data), "# Prvi\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n# Drugi\n\nTekst drugog.\n\n# Sken\n\nTekst iz PDF-a.\n"; got != want {
		t.Fatalf("concatenated file:\n%q\nwant\n%q", got, want)
	}

	// Onto a saved document: it keeps its id, its stored text and its index grow.
	out = call("edit_doc", map[string]any{"doc": first, "append_source": []any{"tmp://sve.md", second}})
	if !strings.Contains(out, "Edited document "+first) || !strings.Contains(out, "from tmp://sve.md, document "+second) {
		t.Fatalf("append to document: %q", out)
	}
	if got := stored(first); !strings.HasPrefix(got, "# Prvi\n\n| A | B |") || strings.Count(got, "# Drugi") != 2 || !strings.Contains(got, "| 1 | 2 |\n\n# Prvi") {
		t.Fatalf("document after append: %q", got)
	}
	if text, _ := store.FullText(ctx, db, mustID(t, first)); !strings.Contains(text, "Tekst iz PDF-a.") {
		t.Fatalf("index after append: %q", text)
	}
	for tool, cases := range map[string]map[*map[string]any]string{
		"edit_doc": {
			{"doc": pdf, "append_source": []any{second}}:                   "not text/Markdown",
			{"doc": second, "append_source": []any{first}, "append": "x"}:  "not both",
			{"doc": second, "append_source": []any{first}, "diff": "@@ x"}: "exactly one operation",
		},
		"edit_file": {
			{"path": "tmp://x.md", "append_source": []any{"u.9999"}}:   "not found",
			{"path": "tmp://x.md", "append_source": []any{"tmp://no"}}: "cannot read source",
			{"path": "ftp://x.md", "append_source": []any{second}}:     "unsupported path",
		},
	} {
		for args, want := range cases {
			if out := call(tool, *args); !strings.HasPrefix(out, "error:") || !strings.Contains(out, want) {
				t.Errorf("%s %v: got %q, want %q", tool, *args, out, want)
			}
		}
	}
	if ok, _ := a.tmpRoot(1).Exists("x.md"); ok {
		t.Error("a failed append created the target")
	}
	if got := stored(second); got != "# Drugi\n\nTekst drugog." {
		t.Errorf("a failed edit changed the document: %q", got)
	}

	// Each edit tool takes the other's kind of target.
	if out := call("edit_doc", map[string]any{"doc": "tmp://sve.md", "old_string": "# Sken", "new_string": "# Skenirano"}); !strings.HasPrefix(out, "tmp://sve.md: ") {
		t.Errorf("edit_doc on a sandbox file: %q", out)
	}
	if out := call("edit_file", map[string]any{"path": second, "append": "Dodatak."}); !strings.Contains(out, "Edited document "+second) {
		t.Errorf("edit_file on a document: %q", out)
	}

	// The reading tools take a sandbox file where they take a document.
	if out := call("read_doc", map[string]any{"doc": "tmp://sve.md", "limit": float64(6)}); !strings.Contains(out, "sve.md (sandbox file tmp://sve.md) — characters 0–6 of") || !strings.Contains(out, "# Prvi") || !strings.Contains(out, "offset=6") {
		t.Errorf("read_doc on a sandbox file: %q", out)
	}
	if out := call("check_text", map[string]any{"doc": "sve.md", "text": "skenirano"}); !strings.Contains(out, "on 1 line(s) of sve.md") || !strings.Contains(out, "[line 11] # Skenirano") {
		t.Errorf("check_text on a sandbox file: %q", out)
	}
	if out := call("check_text", map[string]any{"doc": "tmp://sve.md", "text": "nema toga"}); !strings.HasPrefix(out, "Not found") {
		t.Errorf("check_text miss: %q", out)
	}
	if out := call("read_doc", map[string]any{"doc": second}); !strings.Contains(out, "Dodatak.") {
		t.Errorf("read_doc on a document: %q", out)
	}

	// copy_doc: document -> sandbox file (text), file -> file, refusing to
	// overwrite unless told to.
	if out := call("copy_doc", map[string]any{"source": second, "target": "tmp://rad.md"}); !strings.Contains(out, "Copied document "+second) {
		t.Fatalf("doc to file: %q", out)
	}
	if data, _ := a.tmpRoot(1).Read("rad.md"); string(data) != "# Drugi\n\nTekst drugog.\nDodatak." {
		t.Fatalf("copied text: %q", data)
	}
	call("copy_doc", map[string]any{"source": pdf, "target": "tmp://sken.md"})
	if data, _ := a.tmpRoot(1).Read("sken.md"); !strings.Contains(string(data), "Tekst iz PDF-a.") || strings.Contains(string(data), "%PDF") {
		t.Fatalf("PDF copied as text: %q", data)
	}
	if out := call("copy_doc", map[string]any{"source": first, "target": "tmp://rad.md"}); !strings.Contains(out, "already exists") {
		t.Fatalf("overwrite must be refused: %q", out)
	}
	call("copy_doc", map[string]any{"source": "tmp://rad.md", "target": "storage://kopija.md"})
	if data, _ := a.storageRoot(1).Read("kopija.md"); string(data) != "# Drugi\n\nTekst drugog.\nDodatak." {
		t.Fatalf("file to file: %q", data)
	}
	call("copy_doc", map[string]any{"source": first, "target": "tmp://rad.md", "overwrite": true})
	if data, _ := a.tmpRoot(1).Read("rad.md"); !strings.HasPrefix(string(data), "# Prvi") {
		t.Fatalf("overwrite: %q", data)
	}

	// copy_doc without target: a new saved document. A document is duplicated
	// with its original file; a sandbox file is saved as a text document.
	if out := call("copy_doc", map[string]any{"source": pdf}); !strings.Contains(out, `"Copy of sken.pdf"`) {
		t.Fatalf("duplicate: %q", out)
	}
	dup := ref("Copy of sken.pdf")
	if dup == pdf || stored(dup) != "%PDF-1.4 fake" || stored(pdf) != "%PDF-1.4 fake" {
		t.Fatalf("duplicate %s of %s lost its original file", dup, pdf)
	}
	if text, _ := store.FullText(ctx, db, mustID(t, dup)); !strings.Contains(text, "Tekst iz PDF-a.") {
		t.Fatalf("duplicate's text: %q", text)
	}
	if out := call("copy_doc", map[string]any{"source": "tmp://sken.md", "title": "Sken kao tekst"}); !strings.Contains(out, "Saved as document") {
		t.Fatalf("file to document: %q", out)
	}
	if got := stored(ref("Sken kao tekst")); !strings.Contains(got, "Tekst iz PDF-a.") {
		t.Fatalf("saved copy: %q", got)
	}
	for args, want := range map[*map[string]any]string{
		{"source": second, "target": first}:                 "target must be a sandbox file",
		{"source": "u.9999"}:                                "not found",
		{"source": second, "scope": "project"}:              "project session",
		{"source": "tmp://nema.md", "target": "tmp://y.md"}: "cannot read source",
		{}: "source is required",
	} {
		if out := call("copy_doc", *args); !strings.HasPrefix(out, "error:") || !strings.Contains(out, want) {
			t.Errorf("copy_doc %v: got %q, want %q", *args, out, want)
		}
	}
}

func mustID(t *testing.T, ref string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(ref[strings.IndexByte(ref, '.')+1:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
