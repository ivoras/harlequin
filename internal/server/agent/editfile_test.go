package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

func TestEditFileTool(t *testing.T) {
	ctx := context.Background()
	a := &Agent{DataDir: t.TempDir()}
	rc := &runContext{userID: 1}
	edit := a.buildTools(ctx, rc)["edit_file"].handler

	// append creates the file, then extends it.
	out, _ := edit(ctx, rc, map[string]any{"path": "tmp://doc.md", "append": "# Title\n"})
	if !strings.Contains(out, "appended") {
		t.Fatalf("append: %q", out)
	}
	edit(ctx, rc, map[string]any{"path": "tmp://doc.md", "append": "Hello world.\n"})
	out, _ = edit(ctx, rc, map[string]any{"path": "tmp://doc.md", "old_string": "world", "new_string": "there"})
	if !strings.Contains(out, "replaced 1") {
		t.Fatalf("replace: %q", out)
	}
	out, _ = edit(ctx, rc, map[string]any{"path": "tmp://doc.md", "diff": "@@ -1,2 +1,2 @@\n # Title\n-Hello there.\n+Hi there.\n"})
	if !strings.Contains(out, "applied 1 hunk") {
		t.Fatalf("diff: %q", out)
	}
	data, _ := a.tmpRoot(1).Read("doc.md")
	if string(data) != "# Title\nHi there.\n" {
		t.Fatalf("final content %q", data)
	}

	// Failures change nothing.
	for name, args := range map[string]map[string]any{
		"missing file":  {"path": "tmp://nope.md", "old_string": "a", "new_string": "b"},
		"no match":      {"path": "tmp://doc.md", "old_string": "zzz", "new_string": "b"},
		"bad diff":      {"path": "tmp://doc.md", "diff": "@@\n-nope\n+x\n"},
		"two ops":       {"path": "tmp://doc.md", "append": "x", "old_string": "a", "new_string": "b"},
		"no op":         {"path": "tmp://doc.md"},
		"no path":       {"append": "x"},
		"bad namespace": {"path": "http://x/y", "append": "x"},
	} {
		out, _ := edit(ctx, rc, args)
		if !strings.HasPrefix(out, "error:") {
			t.Errorf("%s: expected error, got %q", name, out)
		}
	}
	data, _ = a.tmpRoot(1).Read("doc.md")
	if string(data) != "# Title\nHi there.\n" {
		t.Fatalf("failed edits modified the file: %q", data)
	}
}

func TestEditDocTool(t *testing.T) {
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
	edit := a.buildTools(ctx, rc)["edit_doc"].handler

	orig := "# Report\n\nFirst paragraph about apples.\n\nSecond paragraph about pears.\n"
	doc, err := store.IngestInto(ctx, sqlDB, types.CreateDocumentRequest{
		Title: "Fruit", URI: "agent://save_doc", Mime: "text/markdown", Content: orig,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	a.storeSavedDocFile(ctx, sqlDB, documents.ScopePersonal, rc, doc, orig)
	ref := "u." + strconv.FormatInt(doc.ID, 10)

	out, _ := edit(ctx, rc, map[string]any{"doc": ref, "old_string": "apples", "new_string": "oranges"})
	if !strings.Contains(out, "Edited document") {
		t.Fatalf("replace: %q", out)
	}
	out, _ = edit(ctx, rc, map[string]any{"doc": ref, "append": "Third paragraph about plums.\n"})
	if !strings.Contains(out, "Edited document") {
		t.Fatalf("append: %q", out)
	}

	// Index, stored file and id all reflect the edits; the id is unchanged.
	text, err := store.FullText(ctx, sqlDB, doc.ID)
	if err != nil || !strings.Contains(text, "oranges") || strings.Contains(text, "apples") || !strings.Contains(text, "plums") {
		t.Fatalf("index not updated: %q (%v)", text, err)
	}
	stored, _, _, _ := store.StoredFile(ctx, sqlDB, doc.ID)
	files, _ := mgr.UserFilesDir(1)
	data, err := os.ReadFile(filepath.Join(files, stored))
	if err != nil || !strings.Contains(string(data), "oranges") || !strings.HasSuffix(string(data), "plums.\n") {
		t.Fatalf("stored file not updated: %q (%v)", data, err)
	}
	var n, chunks int
	sqlDB.QueryRow("SELECT COUNT(*) FROM documents").Scan(&n)
	sqlDB.QueryRow("SELECT COUNT(*) FROM doc_chunks WHERE document_id = ?", doc.ID).Scan(&chunks)
	var fts int
	sqlDB.QueryRow("SELECT COUNT(*) FROM doc_chunks_fts").Scan(&fts)
	if n != 1 || chunks == 0 || fts != chunks {
		t.Fatalf("expected 1 doc with %d chunks indexed once, got docs=%d chunks=%d fts=%d", chunks, n, chunks, fts)
	}

	// Refusals leave the document alone.
	if out, _ := edit(ctx, rc, map[string]any{"doc": ref, "old_string": "nonexistent", "new_string": "x"}); !strings.HasPrefix(out, "error:") {
		t.Errorf("no-match should error: %q", out)
	}
	pdf, _ := store.IngestInto(ctx, sqlDB, types.CreateDocumentRequest{Title: "Scan", URI: "x", Mime: "application/pdf", Content: "pdf text here"}, 1)
	a.storeSavedDocFile(ctx, sqlDB, documents.ScopePersonal, rc, pdf, "pdf text here")
	if _, err := sqlDB.Exec("UPDATE documents SET mime = 'application/pdf' WHERE id = ?", pdf.ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := edit(ctx, rc, map[string]any{"doc": "u." + strconv.FormatInt(pdf.ID, 10), "append": "x"}); !strings.Contains(out, "cannot be edited") {
		t.Errorf("pdf should be refused: %q", out)
	}
	if out, _ := edit(ctx, rc, map[string]any{"doc": "u.9999", "append": "x"}); !strings.HasPrefix(out, "error:") {
		t.Errorf("missing doc should error: %q", out)
	}
}

// save_doc, edit_doc and delete_doc each tell the doc-event bus, so connected
// clients refresh their lists.
func TestDocToolsPublishChangeEvents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	sqlDB := mgr.Shared

	bus := docevents.New()
	owner := bus.Subscribe(1, nil)
	other := bus.Subscribe(2, nil)
	a := &Agent{DataDir: dir, Docs: documents.NewStore(sqlDB, crossCheckFakeEmbedder{dim: 4}), Storage: mgr, DocEvents: bus}
	rc := &runContext{userDB: sqlDB, userID: 1}
	tools := a.buildTools(ctx, rc)

	out, _ := tools["save_doc"].handler(ctx, rc, map[string]any{"title": "T", "content": "Some text to keep.", "scope": "personal", "is_raw": true})
	if !strings.Contains(out, "Saved as document") {
		t.Fatalf("save: %q", out)
	}
	ref := out[strings.Index(out, "u."):]
	ref = ref[:strings.IndexAny(ref, " (")]
	next := func(s *docevents.Sub) (docevents.Event, bool) {
		select {
		case ev := <-s.C:
			return ev, true
		default:
			return docevents.Event{}, false
		}
	}
	if ev, ok := next(owner); !ok || ev.Op != docevents.Created || ev.Scope != "personal" {
		t.Fatalf("save_doc event = %+v ok=%v", ev, ok)
	}
	if out, _ := tools["edit_doc"].handler(ctx, rc, map[string]any{"doc": ref, "append": "more"}); !strings.Contains(out, "Edited document") {
		t.Fatalf("edit: %q", out)
	}
	if ev, ok := next(owner); !ok || ev.Op != docevents.Updated {
		t.Fatalf("edit_doc event = %+v ok=%v", ev, ok)
	}
	if out, _ := tools["delete_doc"].handler(ctx, rc, map[string]any{"doc": ref}); !strings.Contains(out, "Deleted document") {
		t.Fatalf("delete: %q", out)
	}
	if ev, ok := next(owner); !ok || ev.Op != docevents.Deleted {
		t.Fatalf("delete_doc event = %+v ok=%v", ev, ok)
	}
	if _, ok := next(other); ok {
		t.Error("another user received a personal-document event")
	}
}
