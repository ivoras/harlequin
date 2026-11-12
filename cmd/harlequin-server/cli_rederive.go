package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/ivoras/harlequin/internal/server/agent"
	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/server/storage"
)

// runRederiveDocMemories re-runs the document→memory bridge over the documents
// already in every library, storing what it finds in each document's own scope.
// With --delete-auto-shared it first removes every automatically created shared
// memory: before the bridge knew about scopes it filed everything there, so a
// client's facts ended up organisation-wide. It can run while the server is up.
func runRederiveDocMemories(args []string) {
	var rest []string
	deleteShared, dryRun := false, false
	for _, a := range args {
		switch a {
		case "--delete-auto-shared":
			deleteShared = true
		case "--dry-run":
			dryRun = true
		default:
			rest = append(rest, a)
		}
	}
	f, err := parseCmdFlags(rest, false)
	if err != nil || f.email != "" {
		fmt.Fprintln(os.Stderr, "rederive-doc-memories: bad arguments")
		printUsage()
		os.Exit(2)
	}
	cfg, err := config.Load(f.config)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := storage.New(cfg.DataDir, cfg.DBPath, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer store.Close()
	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim, cfg.Embeddings.QueryPrefix, cfg.Embeddings.DocPrefix)
	providers := map[string]*llm.OpenAICompatible{}
	for _, p := range cfg.Providers {
		providers[p.Name] = llm.NewOpenAICompatible(p.Name, p.BaseURL, p.APIKey, p.Model)
	}
	// shortcut: this run's LLM calls are not recorded in the usage tables (the
	// recorder is wired in the server proper); add it if that ever matters.
	router := llm.NewRoutingProvider(providers, cfg.Routing.DefaultProvider, cfg.Routing.FallbackOrder, cfg.Routing.ModelRules, nil, nil)
	router.SetFallbackModel(cfg.Routing.FallbackModel)
	mem := memory.NewStore(store.Shared, embedder)
	if cfg.Memory.ConflictCheckEnabled() {
		mem.SetConflictJudge(router, cfg.Memory.ConflictCandidates)
	}
	docs := documents.NewStore(store.Shared, embedder)
	ag := &agent.Agent{Provider: router, Storage: store, Memory: mem, Docs: docs, MemDefaultTTL: cfg.Memory.DefaultTTL.D()}
	ctx := context.Background()

	// A user to act as where a document does not say who added it.
	users := map[int64]bool{}
	var fallback int64
	rows, err := store.System.QueryContext(ctx, `SELECT id FROM users ORDER BY (role = 'owner') DESC, id`)
	if err != nil {
		log.Fatalf("users: %v", err)
	}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		if fallback == 0 {
			fallback = id
		}
		users[id] = true
	}
	rows.Close()
	if fallback == 0 {
		log.Fatalf("no users")
	}

	if deleteShared {
		var ids []int64
		rows, err := store.Shared.QueryContext(ctx, `SELECT id FROM memories WHERE source = 'auto' ORDER BY id`)
		if err != nil {
			log.Fatalf("shared memories: %v", err)
		}
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		fmt.Printf("shared: %d automatically created memories to delete\n", len(ids))
		if !dryRun {
			for _, id := range ids {
				ref := fmt.Sprintf("s.%d", id)
				if err := mem.Delete(ctx, store.Shared, ref, fallback, true); err != nil {
					log.Fatalf("delete %s: %v", ref, err)
				}
				// Conflicts pairing it with someone's personal memory live in that user's database.
				_ = store.EachUser(ctx, func(_ int64, udb *sql.DB) error {
					_, _ = udb.ExecContext(ctx, `DELETE FROM memory_conflicts WHERE memory_a = ? OR memory_b = ?`, ref, ref)
					return nil
				})
			}
			fmt.Printf("shared: deleted %d\n", len(ids))
		}
	}

	type doc struct {
		id, by int64
		title  string
	}
	// Uploaded documents that have no derived memory yet (so a rerun resumes).
	// Files the assistant wrote itself were never part of the bridge.
	pending := func(db *sql.DB) []doc {
		rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(created_by, 0), COALESCE(title, '') FROM documents
			WHERE COALESCE(uri, '') NOT LIKE 'agent://%'
			  AND id NOT IN (SELECT source_doc FROM memories WHERE source_doc IS NOT NULL) ORDER BY id`)
		if err != nil {
			log.Fatalf("documents: %v", err)
		}
		defer rows.Close()
		var out []doc
		for rows.Next() {
			var d doc
			_ = rows.Scan(&d.id, &d.by, &d.title)
			out = append(out, d)
		}
		return out
	}
	total := 0
	run := func(label, scope string, projectID, owner int64, db *sql.DB) {
		list := pending(db)
		if len(list) == 0 {
			return
		}
		fmt.Printf("%s: %d document(s)\n", label, len(list))
		if dryRun {
			return
		}
		for _, d := range list {
			text, err := docs.FullText(ctx, db, d.id)
			if err != nil {
				log.Printf("%s document %d: %v", label, d.id, err)
				continue
			}
			by := owner
			if by == 0 {
				by = d.by
			}
			if !users[by] {
				by = fallback
			}
			n := ag.ExtractMemoriesFromDoc(ctx, by, scope, projectID, d.id, d.title, text)
			fmt.Printf("  %s document %d: %s\n", label, d.id, n)
			total++
		}
	}
	run("shared", documents.ScopeShared, 0, 0, store.Shared)
	if err := store.EachUser(ctx, func(uid int64, udb *sql.DB) error {
		run(fmt.Sprintf("user %d", uid), documents.ScopePersonal, 0, uid, udb)
		return nil
	}); err != nil {
		log.Fatalf("users: %v", err)
	}
	if err := store.EachProject(ctx, func(pid int64, pdb *sql.DB) error {
		run(fmt.Sprintf("project %d", pid), documents.ScopeProject, pid, 0, pdb)
		return nil
	}); err != nil {
		log.Fatalf("projects: %v", err)
	}
	if dryRun {
		fmt.Println("dry run: nothing was changed")
		return
	}
	fmt.Printf("done: %d document(s) processed\n", total)
}
