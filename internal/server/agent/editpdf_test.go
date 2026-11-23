package agent

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// edit_pdf and edit_doc's save_as end to end: in place by default (with undo),
// out of place on request, and a failed edit never leaves a stray copy.
func TestEditPDFAndSaveAs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	ex, err := pdfextract.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if err := mgr.WithUser(ctx, 1, func(db *sql.DB) error {
		store := documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})
		a := &Agent{DataDir: dir, Docs: store, Storage: mgr, PDFPages: ex}
		rc := &runContext{userDB: db, userID: 1, username: "ana@example.com"}
		tools := a.buildTools(ctx, rc)
		call := func(tool string, args map[string]any) string {
			t.Helper()
			out, err := tools[tool].handler(ctx, rc, args)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			return out
		}
		docs := func(title string) int {
			var n int
			db.QueryRow(`SELECT COUNT(*) FROM documents WHERE title = ?`, title).Scan(&n)
			return n
		}
		add := func(title, mime, content string, file []byte, ext string) (ref, path string) {
			t.Helper()
			doc, err := store.IngestInto(ctx, db, types.CreateDocumentRequest{Title: title, Mime: mime, Content: content}, 1)
			if err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(a.docFilesDir(rc, documents.ScopePersonal), a.storeDocFile(ctx, db, documents.ScopePersonal, rc, doc, file, ext))
			return fmt.Sprintf("u.%d", doc.ID), path
		}
		marked := func(path string) bool {
			t.Helper()
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return bytes.Contains(b, []byte("Highlight"))
		}
		src := pagesPDF("Invoice total 120 EUR", "Payment due in 30 days")
		ref, path := add("inv.pdf", pdfMime, "x", src, ".pdf")
		marks := []any{
			map[string]any{"kind": "highlight", "text": "total 120", "comment": "check"},
			map[string]any{"kind": "box", "text": "nowhere"},
		}

		// Default: in place. The document keeps its id; the file gets the marks.
		out := call("edit_pdf", map[string]any{"doc": ref, "marks": marks})
		if !strings.Contains(out, "in place") || !strings.Contains(out, "NOT done") || !marked(path) || docs("inv.pdf") != 1 {
			t.Fatalf("in place: %s", out)
		}
		// edit_doc undo restores the previous file, once.
		if out = call("edit_doc", map[string]any{"doc": ref, "undo": true}); !strings.Contains(out, "Restored") || marked(path) {
			t.Fatalf("undo: %s (marked=%v)", out, marked(path))
		}
		if b, _ := os.ReadFile(path); !bytes.Equal(b, src) {
			t.Fatal("undo did not restore the original bytes")
		}

		// save_as: a new document; the original stays untouched.
		out = call("edit_pdf", map[string]any{"doc": ref, "marks": marks, "save_as": "inv marked"})
		if !strings.Contains(out, "new document u.") || docs("inv marked.pdf") != 1 || marked(path) {
			t.Fatalf("save_as: %s", out)
		}
		// Nothing marked: no change, no file.
		if out = call("edit_pdf", map[string]any{"doc": ref, "marks": []any{map[string]any{"kind": "highlight", "text": "absent"}}}); !strings.HasPrefix(out, "error: nothing was marked") || marked(path) {
			t.Fatalf("nothing marked: %s", out)
		}
		// A create_pdf document (it has a source sidecar) is marked in place, and
		// the applied marks are recorded in the sidecar for later rebuilds.
		gref, gpath := add("made.pdf", pdfMime, "x", src, ".pdf")
		os.WriteFile(gpath+sidecarExt, []byte(`{"kind":"pdf","format":"markdown","source":"# x"}`), 0o600)
		if out = call("edit_pdf", map[string]any{"doc": gref, "marks": marks}); !strings.Contains(out, "kept with the document's source") || !marked(gpath) {
			t.Fatalf("sidecar doc in place: %s", out)
		}
		if side, _ := os.ReadFile(gpath + sidecarExt); !strings.Contains(string(side), `"text":"total 120"`) || strings.Contains(string(side), "nowhere") {
			t.Fatalf("sidecar marks: %s", side)
		}
		if out = call("edit_pdf", map[string]any{"doc": gref, "marks": marks, "save_as": "made marked"}); !strings.Contains(out, "new document u.") {
			t.Fatalf("sidecar doc as copy: %s", out)
		}

		// Sandbox files: save_to makes a new one (overwrite guarded); with neither it is edited in place.
		out = call("edit_pdf", map[string]any{"doc": ref, "marks": marks, "save_to": "tmp://marked.pdf"})
		if !strings.Contains(out, "new sandbox file tmp://marked.pdf") {
			t.Fatalf("save_to: %s", out)
		}
		if out = call("edit_pdf", map[string]any{"doc": ref, "marks": marks, "save_to": "tmp://marked.pdf"}); !strings.Contains(out, "already exists") {
			t.Fatalf("overwrite guard: %s", out)
		}
		root, _, rel, _ := a.resolveNamespaceRoot(rc, "tmp://marked.pdf")
		before, _ := root.Read(rel)
		if out = call("edit_pdf", map[string]any{"doc": "tmp://marked.pdf", "marks": []any{map[string]any{"kind": "underline", "text": "Payment"}}}); !strings.Contains(out, "in place") {
			t.Fatalf("sandbox in place: %s", out)
		}
		if after, _ := root.Read(rel); bytes.Equal(before, after) {
			t.Fatal("sandbox file was not changed in place")
		}
		if out = call("edit_pdf", map[string]any{"doc": ref, "marks": marks, "save_to": "tmp://x.pdf", "save_as": "y"}); !strings.HasPrefix(out, "error: give either") {
			t.Fatalf("both destinations: %s", out)
		}
		if out = call("edit_pdf", map[string]any{"doc": ref, "marks": marks, "save_to": "https://x/y.pdf"}); !strings.HasPrefix(out, "error: save_to must be a sandbox file") {
			t.Fatalf("bad save_to: %s", out)
		}

		// edit_doc save_as: the edit lands in a new document, the original is unchanged.
		tref, tpath := add("notes.md", "text/markdown", "Payment in 30 days.", []byte("Payment in 30 days."), ".md")
		out = call("edit_doc", map[string]any{"doc": tref, "old_string": "30 days", "new_string": "14 days", "save_as": "notes v2"})
		if !strings.Contains(out, "new document u.") || docs("notes v2") != 1 {
			t.Fatalf("edit_doc save_as: %s", out)
		}
		if b, _ := os.ReadFile(tpath); string(b) != "Payment in 30 days." {
			t.Fatalf("original changed: %q", b)
		}
		// A failing edit with save_as creates nothing.
		if out = call("edit_doc", map[string]any{"doc": tref, "old_string": "no such text", "new_string": "x", "save_as": "notes v3"}); !strings.HasPrefix(out, "error:") || docs("notes v3") != 0 {
			t.Fatalf("failed save_as left a copy: %s (copies: %d)", out, docs("notes v3"))
		}
		// An uploaded PDF cannot be text-edited, with or without save_as, and says what to use.
		if out = call("edit_doc", map[string]any{"doc": ref, "old_string": "a", "new_string": "b", "save_as": "inv text"}); !strings.Contains(out, "edit_pdf") || docs("inv text") != 0 {
			t.Fatalf("pdf text edit: %s", out)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
