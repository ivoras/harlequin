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
	"github.com/ivoras/harlequin/internal/server/storage"
)

// create_excel stores a real .xlsx as the downloadable original, with the
// Markdown as the searchable text, and refuses input without a table.
func TestCreateExcelTool(t *testing.T) {
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
	create := a.buildTools(ctx, rc)["create_excel"].handler

	out, err := create(ctx, rc, map[string]any{"title": "Q1 sales", "content": "# Sales\n| Region | Units |\n|---|---|\n| North | 120 |\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"Q1 sales.xlsx": 1 sheet(s), 1 table(s), 2 row(s)`) {
		t.Fatalf("unexpected result: %q", out)
	}
	var id int64
	var mime string
	if err := db.QueryRow(`SELECT id, mime FROM documents WHERE title = 'Q1 sales.xlsx'`).Scan(&id, &mime); err != nil {
		t.Fatal(err)
	}
	if mime != xlsxMime {
		t.Fatalf("mime %q", mime)
	}
	stored, _, _, err := store.StoredFile(ctx, db, id)
	if err != nil || !strings.HasSuffix(stored, ".xlsx") || strings.HasSuffix(stored, ".xlsx.xlsx") {
		t.Fatalf("stored path %q (%v)", stored, err)
	}
	files, _ := mgr.UserFilesDir(1)
	data, err := os.ReadFile(filepath.Join(files, stored))
	if err != nil || !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("stored file is not a zip (%v)", err)
	}
	text, _ := store.FullText(ctx, db, id)
	if !strings.Contains(text, "| North | 120 |") {
		t.Fatalf("searchable text: %q", text)
	}

	// The Markdown can also come from a sandbox file or from a saved document
	// (here the workbook just created, whose text is its tables).
	if err := a.tmpRoot(1).Write("t.md", []byte("| City | Pop |\n|---|---|\n| Zagreb | 767131 |\n")); err != nil {
		t.Fatal(err)
	}
	for title, src := range map[string]string{"from tmp": "tmp://t.md", "from bare path": "t.md", "from doc": "s." + strconv.FormatInt(id, 10), "from attached": `@"s.` + strconv.FormatInt(id, 10) + `/Q1 sales.xlsx"`} {
		if out, _ := create(ctx, rc, map[string]any{"title": title, "source": src}); !strings.Contains(out, "1 sheet(s), 1 table(s), 2 row(s)") {
			t.Errorf("source %s: %q", src, out)
		}
	}
	if out, _ := create(ctx, rc, map[string]any{"title": "x", "source": "s.9999"}); !strings.Contains(out, "not found") {
		t.Errorf("missing document: %q", out)
	}

	// A header with no data rows (e.g. a call cut off mid-table) is refused, not saved.
	for _, md := range []string{"| a | b", "| a | b |\n", "| a | b |\n| --- | --- |\n", "# S\n| a |\n| 1 |\n\n# T\n| x |\n"} {
		if out, _ := create(ctx, rc, map[string]any{"title": "x", "content": md}); !strings.Contains(out, "no data rows") {
			t.Errorf("content %q should be rejected for having no data rows, got %q", md, out)
		}
	}
	if out, _ := create(ctx, rc, map[string]any{"title": "x", "content": "no tables here"}); !strings.HasPrefix(out, "error:") {
		t.Fatalf("expected an error without tables, got %q", out)
	}
	if out, _ := create(ctx, rc, map[string]any{"title": "x", "content": "| a |\n| 1 |\n", "scope": "shared"}); !strings.Contains(out, "owner or admin") {
		t.Fatalf("shared scope should be refused for a plain user, got %q", out)
	}
}
