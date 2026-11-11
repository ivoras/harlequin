package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mdpdf"
	"github.com/ivoras/harlequin/internal/server/mdword"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type genHarness struct {
	t     *testing.T
	ctx   context.Context
	a     *Agent
	rc    *runContext
	db    *sql.DB
	store *documents.Store
	files string
}

func newGenHarness(t *testing.T) *genHarness {
	t.Helper()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })
	store := documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})
	files, _ := mgr.UserFilesDir(1)
	return &genHarness{t: t, ctx: context.Background(), a: &Agent{DataDir: dir, Docs: store, Storage: mgr},
		rc: &runContext{userDB: mgr.Shared, userID: 1}, db: mgr.Shared, store: store, files: files}
}

func (h *genHarness) call(tool string, args map[string]any) string {
	h.t.Helper()
	out, err := h.a.buildTools(h.ctx, h.rc)[tool].handler(h.ctx, h.rc, args)
	if err != nil {
		h.t.Fatalf("%s: %v", tool, err)
	}
	return out
}

// create runs a create_* tool and returns the new document's id and stored file.
func (h *genHarness) create(tool, title, content string) (int64, string) {
	h.t.Helper()
	if out := h.call(tool, map[string]any{"title": title, "content": content}); !strings.HasPrefix(out, "Created") {
		h.t.Fatalf("%s: %s", tool, out)
	}
	var id int64
	if err := h.db.QueryRow(`SELECT id FROM documents WHERE title LIKE ? ORDER BY id DESC`, title+".%").Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return id, h.stored(id)
}

func (h *genHarness) stored(id int64) string {
	name, _, _, _ := h.store.StoredFile(h.ctx, h.db, id)
	return filepath.Join(h.files, name)
}

func ref(id int64) string { return "u." + strconv.FormatInt(id, 10) }

func (h *genHarness) text(id int64) string {
	s, _ := h.store.FullText(h.ctx, h.db, id)
	return s
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// zipText joins every entry of a zip (a docx or xlsx) so a marker can be looked for.
func zipText(t *testing.T, data []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, f := range zr.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		sb.Write(b)
	}
	return sb.String()
}

func sidecarSource(t *testing.T, stored string) genSource {
	t.Helper()
	s, err := readSidecar(readFile(t, stored+sidecarExt))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	sheetMD = "# Sales\n| Region | Units |\n|---|---|\n| North | 120 |\n"
	prose   = "# Report\n\nFirst alpha paragraph.\n\nSecond beta paragraph.\n"
)

func TestEditGeneratedDocuments(t *testing.T) {
	type kind struct {
		name, tool, initial string
		check               func(t *testing.T, data []byte, marker string)
		// edits: append, replace, insert — each adds a marker the next one builds on
		appendText, replOld, replNew, insAfter, insText string
		markers                                         [3]string
	}
	inZip := func(t *testing.T, data []byte, marker string) {
		t.Helper()
		if !strings.Contains(zipText(t, data), marker) {
			t.Fatalf("stored file lacks %q", marker)
		}
	}
	kinds := []kind{
		{name: "excel", tool: "create_excel", initial: sheetMD, check: inZip,
			appendText: "| South | 80 |\n", replOld: "North", replNew: "Northern", insAfter: "| Northern | 120 |", insText: "| East | 5 |",
			markers: [3]string{"South", "Northern", "East"}},
		{name: "word", tool: "create_word", initial: prose, check: inZip,
			appendText: "Third gamma paragraph.\n", replOld: "alpha", replNew: "delta", insAfter: "Second beta paragraph.", insText: "Inserted epsilon paragraph.",
			markers: [3]string{"gamma", "delta", "epsilon"}},
		{name: "pdf", tool: "create_pdf", initial: prose,
			check: func(t *testing.T, data []byte, marker string) {
				if !bytes.HasPrefix(data, []byte("%PDF-")) {
					t.Fatal("stored file is not a PDF")
				}
			},
			appendText: "\n\\newpage\n\nThird gamma page.\n", replOld: "alpha", replNew: "delta", insAfter: "Second beta paragraph.", insText: "Inserted epsilon paragraph.",
			markers: [3]string{"gamma", "delta", "epsilon"}},
	}
	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			h := newGenHarness(t)
			var err error
			switch k.name {
			case "pdf":
				if h.a.PDF, err = mdpdf.New(""); err != nil {
					t.Skip(err)
				}
			case "word":
				if h.a.Word, err = mdword.New(""); err != nil {
					t.Skip(err)
				}
			}
			id, stored := h.create(k.tool, "Doc "+k.name, k.initial)
			if s := sidecarSource(t, stored); s.Kind != k.name || s.Source != k.initial || s.Landscape {
				t.Fatalf("sidecar after create: %+v", s)
			}
			prevData := readFile(t, stored)
			pages := mdpdf.Pages(prevData)
			steps := []map[string]any{
				{"doc": ref(id), "append": k.appendText},
				{"doc": ref(id), "old_string": k.replOld, "new_string": k.replNew},
				{"doc": ref(id), "insert": k.insText, "after": k.insAfter},
			}
			for i, args := range steps {
				out := h.call("edit_doc", args)
				if !strings.HasPrefix(out, "Edited "+k.name+" document") || !strings.Contains(out, "file was rebuilt") {
					t.Fatalf("step %d: %s", i, out)
				}
				data := readFile(t, stored)
				if bytes.Equal(data, prevData) {
					t.Fatalf("step %d: stored file unchanged", i)
				}
				prevData = data
				k.check(t, data, k.markers[i])
				if !strings.Contains(h.text(id), k.markers[i]) || !strings.Contains(sidecarSource(t, stored).Source, k.markers[i]) {
					t.Fatalf("step %d: index or sidecar lacks %q", i, k.markers[i])
				}
				if k.name == "pdf" && i == 0 {
					if n := mdpdf.Pages(data); n != pages+1 || !strings.Contains(out, strconv.Itoa(n)+" page(s)") {
						t.Fatalf("pages %d -> %d: %s", pages, n, out)
					}
				}
			}
			var n int
			h.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&n)
			if n != 1 {
				t.Fatalf("%d documents", n)
			}
			// Searchable text is what a fresh create of the final source would store.
			final := sidecarSource(t, stored).Source
			id2, _ := h.create(k.tool, "Fresh "+k.name, final)
			if h.text(id) != h.text(id2) {
				t.Fatalf("index differs from a fresh create:\n%q\n%q", h.text(id), h.text(id2))
			}

			// A failed edit changes nothing.
			fileBefore, sideBefore, textBefore := readFile(t, stored), readFile(t, stored+sidecarExt), h.text(id)
			prevBefore := readFile(t, stored+prevExt)
			bad := []map[string]any{{"doc": ref(id), "old_string": "no such passage", "new_string": "x"}, {"doc": ref(id), "insert": "x", "after": "no such passage"}}
			if k.name == "excel" { // removes every table
				bad = append(bad, map[string]any{"doc": ref(id), "diff": "@@ -1,6 +1,1 @@\n-# Sales\n-| Region | Units |\n-|---|---|\n-| Northern | 120 |\n-| East | 5 |\n-| South | 80 |\n+nothing\n"})
			}
			for _, args := range bad {
				if out := h.call("edit_doc", args); !strings.HasPrefix(out, "error:") {
					t.Fatalf("expected error, got %q", out)
				}
			}
			if !bytes.Equal(fileBefore, readFile(t, stored)) || !bytes.Equal(sideBefore, readFile(t, stored+sidecarExt)) ||
				textBefore != h.text(id) || !bytes.Equal(prevBefore, readFile(t, stored+prevExt)) {
				t.Fatal("a failed edit changed the document")
			}

			// Undo restores the version before the last edit, once.
			if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "Restored") {
				t.Fatalf("undo: %s", out)
			}
			if strings.Contains(h.text(id), k.markers[2]) || !strings.Contains(h.text(id), k.markers[1]) ||
				strings.Contains(sidecarSource(t, stored).Source, k.markers[2]) || !bytes.Equal(readFile(t, stored), prevBefore) {
				t.Fatal("undo did not restore the previous version")
			}
			if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "no previous version") {
				t.Fatalf("second undo: %s", out)
			}

			// A copy stays editable; deleting removes every companion file.
			if out := h.call("copy_doc", map[string]any{"source": ref(id)}); !strings.Contains(out, "Copied") {
				t.Fatal(out)
			}
			var copyID int64
			h.db.QueryRow(`SELECT MAX(id) FROM documents`).Scan(&copyID)
			if out := h.call("edit_doc", map[string]any{"doc": ref(copyID), "append": k.appendText}); !strings.HasPrefix(out, "Edited") {
				t.Fatalf("edit copy: %s", out)
			}
			if strings.Contains(h.text(id), k.markers[0]+" zzz") || !strings.Contains(sidecarSource(t, h.stored(id)).Source, k.markers[1]) {
				t.Fatal("editing the copy touched the original")
			}
			if err := h.store.DeleteFrom(h.ctx, h.db, h.files, copyID); err != nil {
				t.Fatal(err)
			}
			ents, _ := os.ReadDir(h.files)
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), strconv.FormatInt(copyID, 10)+"-") {
					t.Errorf("left behind after delete: %s", e.Name())
				}
			}
			if err := h.store.DeleteFrom(h.ctx, h.db, h.files, id); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"", prevExt, sidecarExt, sidecarExt + prevExt} {
				if _, err := os.Stat(stored + suffix); err == nil {
					t.Errorf("%s survived delete", filepath.Base(stored+suffix))
				}
			}
		})
	}
}

func TestEditGeneratedWithoutRenderer(t *testing.T) {
	h := newGenHarness(t)
	var err error
	if h.a.PDF, err = mdpdf.New(""); err != nil {
		t.Skip(err)
	}
	id, _ := h.create("create_pdf", "P", prose)
	h.a.PDF = nil
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "append": "x"}); !strings.Contains(out, "not set up on this server") {
		t.Fatal(out)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "no previous version") {
		t.Fatal(out) // never edited, and undo needs no renderer
	}
}

func TestUndoRules(t *testing.T) {
	h := newGenHarness(t)
	id, stored := h.create("create_excel", "Book", sheetMD)
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.HasPrefix(out, "error:") || !strings.Contains(out, "no previous version") {
		t.Fatalf("never edited: %s", out)
	}
	if out := h.call("edit_doc", map[string]any{"doc": "tmp://x.md", "undo": true}); !strings.Contains(out, "not available for sandbox files") {
		t.Fatalf("sandbox: %s", out)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true, "append": "x"}); !strings.Contains(out, "cannot be combined") {
		t.Fatalf("combined: %s", out)
	}
	h.call("edit_doc", map[string]any{"doc": ref(id), "append": "| South | 80 |\n"})
	if _, err := os.Stat(stored + prevExt); err != nil {
		t.Fatal("no .prev after an edit")
	}

	// Text documents undo too.
	doc, _ := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: "Note", URI: "agent://save_doc", Mime: "text/markdown", Content: "one\n"}, 1)
	h.a.storeSavedDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, "one\n")
	h.call("edit_doc", map[string]any{"doc": ref(doc.ID), "append": "two\n"})
	if out := h.call("edit_doc", map[string]any{"doc": ref(doc.ID), "undo": true}); !strings.Contains(out, "Restored") {
		t.Fatal(out)
	}
	if got := string(readFile(t, h.stored(doc.ID))); got != "one\n" || strings.Contains(h.text(doc.ID), "two") {
		t.Fatalf("text undo: %q / %q", got, h.text(doc.ID))
	}
}

func TestUploadedFilesRefused(t *testing.T) {
	h := newGenHarness(t)
	for mime, want := range map[string]string{
		"application/pdf": "uploaded PDF",
		docxMime:          "is not a .docx",       // the stored bytes are not a Word file
		xlsxMime:          "not a readable .xlsx", // the stored bytes are not a workbook
	} {
		doc, err := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: "Up " + strconv.Itoa(len(mime)), URI: "upload", Mime: mime, Content: "text"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		h.a.storeDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, []byte("data"), ".bin")
		out := h.call("edit_doc", map[string]any{"doc": ref(doc.ID), "append": "x"})
		if !strings.HasPrefix(out, "error:") || !strings.Contains(out, want) {
			t.Errorf("%s: %s", mime, out)
		}
		if mime == "application/pdf" && (!strings.Contains(out, "create_pdf") || !strings.Contains(out, "pdf_pages")) {
			t.Errorf("pdf refusal should point to create_pdf and pdf_pages: %s", out)
		}
	}
}

func TestSidecarFormat(t *testing.T) {
	h := newGenHarness(t)
	_, stored := h.create("create_excel", "Fmt", sheetMD)
	var m map[string]any
	if err := json.Unmarshal(readFile(t, stored+sidecarExt), &m); err != nil {
		t.Fatal(err)
	}
	if m["kind"] != "excel" || m["format"] != "markdown" || m["landscape"] != false || m["source"] != sheetMD {
		t.Fatalf("%v", m)
	}
	if !strings.HasSuffix(stored, ".xlsx") {
		t.Fatal(stored)
	}
}
