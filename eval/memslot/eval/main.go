// Command eval loads the memory-slot dataset into a fresh shared database
// (embedding content + humanized slot keys via the configured embeddings
// endpoint), then measures recall@k and MRR for the sloppy query set under:
//
//	baseline  - FTS + content-vector RRF (no slot leg)
//	option A  - baseline + slot-key RRF leg at weight 1.0
//	option B  - baseline + slot-key RRF leg at swept weights
//
//	go run ./eval/memslot/eval --config server.yaml
//
// Requires the embeddings endpoint in the config to be reachable.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/memory"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type memRecord struct {
	ID      string `json:"id"`
	Scope   string `json:"scope"`
	Content string `json:"content"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

type query struct {
	ID        string   `json:"id"`
	Query     string   `json:"query"`
	TargetIDs []string `json:"target_ids"`
	Key       string   `json:"key"`
}

func main() {
	cfgPath := flag.String("config", "server.yaml", "server config (for embeddings settings)")
	dataDir := flag.String("data", "eval/memslot/data", "dataset directory")
	limit := flag.Int("limit", 10, "results per query")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	must(err)

	var mems []memRecord
	loadJSON(filepath.Join(*dataDir, "memories.json"), &mems)
	var queries []query
	loadJSON(filepath.Join(*dataDir, "queries.json"), &queries)
	fmt.Printf("loaded %d memories, %d queries\n", len(mems), len(queries))

	// Fresh temp shared database.
	tmp, err := os.MkdirTemp("", "memslot-eval-*")
	must(err)
	defer os.RemoveAll(tmp)
	database, err := db.Open(filepath.Join(tmp, "shared.db"), db.Shared, cfg.Embeddings.Dim)
	must(err)
	defer database.Close()

	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	store := memory.NewStore(database, embedder)
	ctx := context.Background()

	// Load: each memory becomes a shared memory + a directly-attached slot.
	fmt.Print("indexing")
	start := time.Now()
	idMap := make(map[string]string, len(mems)) // dataset id -> composite id
	for i, m := range mems {
		rec, err := store.Add(ctx, nil, types.CreateMemoryRequest{Scope: "shared", Content: m.Content, Source: "eval"}, 0)
		must(err)
		must(store.AddSlot(ctx, nil, rec.ID, m.Key, m.Value))
		idMap[m.ID] = rec.ID
		if (i+1)%100 == 0 {
			fmt.Printf(" %d", i+1)
		}
	}
	fmt.Printf("\nindexed in %s\n\n", time.Since(start).Round(time.Second))

	weights := []float64{0, 0.25, 0.5, 1.0, 2.0, 4.0}
	fmt.Printf("%-22s %7s %7s %7s %7s %7s\n", "variant", "R@1", "R@3", "R@5", "R@10", "MRR")
	for _, w := range weights {
		m := evalWeight(ctx, store, queries, idMap, *limit, w)
		fmt.Printf("%-22s %7.3f %7.3f %7.3f %7.3f %7.3f\n", label(w), m.r1, m.r3, m.r5, m.r10, m.mrr)
	}
}

type metrics struct{ r1, r3, r5, r10, mrr float64 }

func evalWeight(ctx context.Context, store *memory.Store, queries []query, idMap map[string]string, limit int, w float64) metrics {
	var r1, r3, r5, r10, rr float64
	for _, q := range queries {
		targets := map[string]bool{}
		for _, t := range q.TargetIDs {
			if cid, ok := idMap[t]; ok {
				targets[cid] = true
			}
		}
		res, err := store.SearchTuned(ctx, nil, q.Query, 0, "shared", limit, w)
		must(err)
		rank := 0
		for i, r := range res {
			if targets[r.ID] {
				rank = i + 1
				break
			}
		}
		if rank == 0 {
			continue
		}
		rr += 1.0 / float64(rank)
		if rank <= 1 {
			r1++
		}
		if rank <= 3 {
			r3++
		}
		if rank <= 5 {
			r5++
		}
		if rank <= 10 {
			r10++
		}
	}
	n := float64(len(queries))
	return metrics{r1 / n, r3 / n, r5 / n, r10 / n, rr / n}
}

func label(w float64) string {
	switch {
	case w == 0:
		return "baseline (no slot)"
	case w == 1:
		return "A: slot w=1.0"
	default:
		return fmt.Sprintf("B: slot w=%.2f", w)
	}
}

func loadJSON(path string, v any) {
	b, err := os.ReadFile(path)
	must(err)
	must(json.Unmarshal(b, v))
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
