package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type fakeProjects struct{ list []types.Project }

func (f *fakeProjects) List(context.Context, int64) ([]types.Project, error) { return f.list, nil }
func (f *fakeProjects) Create(_ context.Context, name string, _ int64) (*types.Project, error) {
	p := types.Project{ID: int64(len(f.list) + 1), Name: name}
	f.list = append(f.list, p)
	return &p, nil
}

func TestProjectTools(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	store := documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})
	fp := &fakeProjects{}
	a := &Agent{Docs: store, Storage: mgr, Projects: fp}

	// Ordinary users do not get the tools.
	plain := a.buildTools(ctx, &runContext{userDB: mgr.Shared, userID: 1})
	if plain["create_project"].handler != nil || plain["move_doc"].handler != nil {
		t.Fatal("create_project/move_doc must be owner/admin only")
	}

	rc := &runContext{userDB: mgr.Shared, userID: 1, canShareMemory: true}
	tools := a.buildTools(ctx, rc)
	create, move := tools["create_project"].handler, tools["move_doc"].handler
	if create == nil || move == nil {
		t.Fatal("elevated user should have create_project and move_doc")
	}

	out, _ := create(ctx, rc, map[string]any{"name": "  Acme   leasing "})
	if !strings.Contains(out, `"Acme leasing" (p1)`) {
		t.Fatalf("create: %q", out)
	}
	if out, _ = create(ctx, rc, map[string]any{"name": "acme LEASING"}); !strings.HasPrefix(out, "error:") {
		t.Fatalf("duplicate name should be refused: %q", out)
	}
	if out, _ = create(ctx, rc, map[string]any{"name": " "}); !strings.HasPrefix(out, "error:") {
		t.Fatalf("empty name should be refused: %q", out)
	}

	// A personal document with a stored file, moved into the project by name.
	doc, err := store.IngestInto(ctx, mgr.Shared, types.CreateDocumentRequest{
		Title: "Invoice.pdf", Mime: "text/plain", Content: "Interest for November is 294.80 EUR.",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref := "u." + itoa(doc.ID)
	out, err = move(ctx, rc, map[string]any{"doc": ref, "to_project": "acme leasing", "mode": "copy"})
	if err != nil || !strings.Contains(out, "Copied") || !strings.Contains(out, "p1.") {
		t.Fatalf("copy: %q %v", out, err)
	}
	if _, errMsg := a.loadDocRef(ctx, rc, ref); errMsg != "" {
		t.Fatalf("copy must keep the original: %s", errMsg)
	}
	out, err = move(ctx, rc, map[string]any{"doc": ref, "to_project": "p1"})
	if err != nil || !strings.Contains(out, "Moved") {
		t.Fatalf("move: %q %v", out, err)
	}
	if _, errMsg := a.loadDocRef(ctx, rc, ref); errMsg == "" {
		t.Fatal("move must delete the original")
	}
	// Both copies are in the project corpus and searchable text came along.
	var n int
	_ = mgr.WithProject(ctx, 1, func(p *sql.DB) error {
		return p.QueryRow(`SELECT COUNT(*) FROM documents WHERE title = 'Invoice.pdf'`).Scan(&n)
	})
	if n != 2 {
		t.Fatalf("project should hold the copy and the moved doc, has %d", n)
	}

	// Unknown project, same project, shared doc.
	if out, _ = move(ctx, rc, map[string]any{"doc": "u.999", "to_project": "Nope"}); !strings.HasPrefix(out, "error:") {
		t.Fatalf("unknown project: %q", out)
	}
	shared, _ := store.IngestInto(ctx, store.SharedDB(), types.CreateDocumentRequest{Title: "Org", Mime: "text/plain", Content: "x y z."}, 1)
	_ = shared // shared and personal corpora are the same db in this harness; the s.N path is covered by scope check below
	if out, _ = move(ctx, rc, map[string]any{"doc": "s.1", "to_project": "p1"}); !strings.HasPrefix(out, "error:") {
		t.Fatalf("shared docs must not move: %q", out)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
