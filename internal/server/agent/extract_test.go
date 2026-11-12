package agent

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/agent/memextract"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// cannedProvider answers every call with the same text and records the system
// prompt it was given.
type cannedProvider struct {
	reply  string
	system *string
}

func (cannedProvider) Name() string { return "canned" }
func (p cannedProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	if p.system != nil && len(req.Messages) > 0 {
		*p.system = req.Messages[0].Content
	}
	ch := make(chan llm.Chunk, 2)
	ch <- llm.Chunk{TextDelta: p.reply}
	ch <- llm.Chunk{Done: true, Model: "fake"}
	close(ch)
	return ch, nil
}

func memContents(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT content FROM memories ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		out = append(out, c)
	}
	return out
}

// Memories derived from a document are stored in the document's own scope,
// whatever scope the model names, reference the document, and are capped.
func TestDocumentMemoriesFollowTheDocumentsScope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	emb := crossCheckFakeEmbedder{dim: 4}
	var system string
	// Seven candidates: the model calls most of them "shared"; one is below the
	// confidence bar.
	reply := `{"memories":[
		{"content":"Primjer d.o.o. has tax number 12345678901","confidence":9,"scope":"shared"},
		{"content":"Primjer d.o.o. is registered at Ulica 1, Zagreb","confidence":9,"scope":"shared"},
		{"content":"Contract 77 between Primjer d.o.o. and Uzorak d.d. started on 1.1.2026","confidence":8,"scope":"shared"},
		{"content":"Contract 77 runs for 36 months","confidence":8},
		{"content":"Uzorak d.d. is represented by its director","confidence":7,"scope":"user"},
		{"content":"Uzorak d.d. banks with an account ending 0001","confidence":7,"scope":"shared"},
		{"content":"Something the model was unsure about","confidence":4,"scope":"shared"}]}`
	a := &Agent{
		DataDir: dir, Storage: mgr, Docs: documents.NewStore(mgr.Shared, emb),
		Memory: memory.NewStore(mgr.Shared, emb), Provider: cannedProvider{reply: reply, system: &system},
	}
	const pid = 3
	ingest := func(db *sql.DB, title string) int64 {
		t.Helper()
		d, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{Title: title, Content: "text of " + title}, 1)
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}

	// A project document: everything lands in the project, nothing in shared.
	var projDoc int64
	if err := mgr.WithProject(ctx, pid, func(pdb *sql.DB) error { projDoc = ingest(pdb, "ugovor.pdf"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := a.ExtractMemoriesFromDoc(ctx, 1, documents.ScopeProject, pid, projDoc, "ugovor.pdf", "text"); got != "5 memory(ies) (5 project)" {
		t.Fatalf("project document: stored %s", got)
	}
	if !strings.Contains(system, "do NOT copy its contents into memory") || strings.Contains(system, `"scope"`) {
		t.Errorf("the document prompt was not used, or still asks for a scope")
	}
	if n := len(memContents(t, mgr.Shared)); n != 0 {
		t.Fatalf("%d memories leaked into shared memory", n)
	}
	_ = mgr.WithProject(ctx, pid, func(pdb *sql.DB) error {
		got := memContents(t, pdb)
		if len(got) != memextract.MaxDocumentMemories || strings.Contains(strings.Join(got, "|"), "unsure") {
			t.Errorf("project memories: %q", got)
		}
		// The two lowest-confidence survivors compete for the last slot; the
		// 9s and 8s are always kept.
		for _, want := range []string{"tax number", "registered at", "started on", "36 months"} {
			if !strings.Contains(strings.Join(got, "|"), want) {
				t.Errorf("the higher-confidence fact %q was dropped: %q", want, got)
			}
		}
		mems, err := a.Memory.ProjectList(ctx, pdb, 10)
		if err != nil || len(mems) != 5 {
			t.Fatalf("list: %d, %v", len(mems), err)
		}
		for _, m := range mems {
			if m.SourceDoc != "p."+itoa(projDoc) || m.SourceDocTitle != "ugovor.pdf" || m.Source != "auto" || m.Scope != "project" {
				t.Errorf("memory %s: source doc %q %q, source %q, scope %q", m.ID, m.SourceDoc, m.SourceDocTitle, m.Source, m.Scope)
			}
		}
		// A second run adds nothing: the same facts are already there.
		if got := a.ExtractMemoriesFromDoc(ctx, 1, documents.ScopeProject, pid, projDoc, "ugovor.pdf", "text"); got != "0 memories" {
			t.Errorf("re-run stored %s", got)
		}
		return nil
	})

	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		// A personal document: the user's own memory, even for "shared" candidates.
		doc := ingest(udb, "moj-ugovor.pdf")
		if got := a.ExtractMemoriesFromDoc(ctx, 1, documents.ScopePersonal, 0, doc, "moj-ugovor.pdf", "text"); !strings.HasSuffix(got, "user)") || strings.Contains(got, "shared") {
			t.Fatalf("personal document: stored %s", got)
		}
		if n := len(memContents(t, mgr.Shared)); n != 0 {
			t.Fatalf("%d memories leaked into shared memory", n)
		}
		mems, _ := a.Memory.List(ctx, udb, 1, "user", 10)
		if len(mems) == 0 || mems[0].SourceDoc != "u."+itoa(doc) || mems[0].SourceDocTitle != "moj-ugovor.pdf" {
			t.Fatalf("user memories: %+v", mems)
		}
		// memory_search tells the model where a derived memory comes from.
		res, err := a.Memory.SearchFused(ctx, udb, nil, "tax number", 1, 5)
		if err != nil || len(res) == 0 {
			t.Fatalf("search: %v %v", res, err)
		}
		if out := renderResults(res); !strings.Contains(out, `(from document u.`+itoa(doc)+` "moj-ugovor.pdf")`) {
			t.Errorf("search output: %s", out)
		}
		// The reference survives the document; it is shown as deleted.
		if err := a.Docs.DeleteFrom(ctx, udb, "", doc); err != nil {
			t.Fatal(err)
		}
		if m, _ := a.Memory.Get(ctx, udb, mems[0].ID, 1); m == nil || m.SourceDoc != "u."+itoa(doc) || m.SourceDocTitle != "" {
			t.Errorf("after deleting the document: %+v", m)
		}
		return nil
	})

	// A document in the shared library is the one case that writes shared memory.
	sharedDoc := ingest(mgr.Shared, "pravilnik.pdf")
	if got := a.ExtractMemoriesFromDoc(ctx, 1, documents.ScopeShared, 0, sharedDoc, "pravilnik.pdf", "text"); !strings.HasSuffix(got, "shared)") {
		t.Fatalf("shared document: stored %s", got)
	}
}

// A turn in a project session files each fact where it belongs: the project's
// subject in project memory, a personal preference in the user's own.
func TestTurnMemoriesInAProjectSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	emb := crossCheckFakeEmbedder{dim: 4}
	var system string
	reply := `{"memories":[
		{"content":"The client's audit is due on 31.3.2027","confidence":9,"scope":"project"},
		{"content":"The user prefers answers in Croatian","confidence":9,"scope":"user"},
		{"content":"The organisation uses GitLab","confidence":8,"scope":"shared"}]}`
	a := &Agent{DataDir: dir, Storage: mgr, Memory: memory.NewStore(mgr.Shared, emb), Provider: cannedProvider{reply: reply, system: &system}}
	const pid = 4
	_ = mgr.WithProject(ctx, pid, func(*sql.DB) error { return nil })

	// An ordinary user cannot write shared memory: that fact stays personal.
	a.extractMemories(ctx, pid, 1, "u", "a", nil, false)
	if !strings.Contains(system, `"user"|"project"|"shared"`) || !strings.Contains(system, `never "shared"`) {
		t.Errorf("the project-session prompt was not used")
	}
	_ = mgr.WithProject(ctx, pid, func(pdb *sql.DB) error {
		if got := memContents(t, pdb); len(got) != 1 || !strings.Contains(got[0], "audit") {
			t.Errorf("project memory: %q", got)
		}
		return nil
	})
	_ = mgr.WithUser(ctx, 1, func(udb *sql.DB) error {
		if got := memContents(t, udb); len(got) != 2 {
			t.Errorf("user memory: %q", got)
		}
		return nil
	})
	if got := memContents(t, mgr.Shared); len(got) != 0 {
		t.Errorf("shared memory: %q", got)
	}

	// Outside a project there is no "project" scope to offer or to store into.
	a.extractMemories(ctx, 0, 2, "u", "a", nil, true)
	if strings.Contains(system, `"project"`) {
		t.Errorf("the plain prompt offers the project scope")
	}
	_ = mgr.WithUser(ctx, 2, func(udb *sql.DB) error {
		if got := memContents(t, udb); len(got) != 2 { // the "project" fact falls back to personal
			t.Errorf("user 2 memory: %q", got)
		}
		return nil
	})
	if got := memContents(t, mgr.Shared); len(got) != 1 || !strings.Contains(got[0], "GitLab") {
		t.Errorf("shared memory for an admin: %q", got)
	}
}
