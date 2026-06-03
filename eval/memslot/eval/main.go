// Command eval loads the memory-slot dataset into a fresh shared database
// (embedding content + humanized slot keys via the configured embeddings
// endpoint), then measures recall@k and MRR for the sloppy query set under:
//
//	baseline  - FTS + content-vector RRF (no slot leg)
//	option A  - baseline + slot-key RRF leg at weight 1.0
//	option B  - baseline + slot-key RRF leg at swept weights
//	option C  - baseline + slot leg where the slot vector embeds key+value
//	            (swept weights), indexed into a second store
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

	embedder := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	ctx := context.Background()

	// Two index builds differing only in the slot vector's embed text:
	//   key store - humanized key (baseline / A / B)
	//   kv store  - humanized key + value (option C)
	keyStore, keyMap, closeKey := buildStore(ctx, cfg, embedder, mems,
		func(k, v string) string { return memory.HumanizeKey(k) }, "key")
	defer closeKey()
	kvStore, kvMap, closeKV := buildStore(ctx, cfg, embedder, mems,
		func(k, v string) string { return memory.HumanizeKey(k) + " " + v }, "key+value")
	defer closeKV()

	fmt.Printf("\n%-24s %7s %7s %7s %7s %7s\n", "variant", "R@1", "R@3", "R@5", "R@10", "MRR")
	row := func(name string, st *memory.Store, idMap map[string]string, w float64) {
		m := evalWeight(ctx, st, queries, idMap, *limit, w)
		fmt.Printf("%-24s %7.3f %7.3f %7.3f %7.3f %7.3f\n", name, m.r1, m.r3, m.r5, m.r10, m.mrr)
	}
	row("baseline (no slot)", keyStore, keyMap, 0)
	for _, w := range []float64{0.25, 0.5, 1.0, 2.0, 4.0} {
		name := fmt.Sprintf("B: key w=%.2f", w)
		if w == 1.0 {
			name = "A: key w=1.0"
		}
		row(name, keyStore, keyMap, w)
	}
	for _, w := range []float64{0.25, 0.5, 1.0, 2.0} {
		row(fmt.Sprintf("C: key+value w=%.2f", w), kvStore, kvMap, w)
	}
}

// buildStore loads the dataset into a fresh temp shared DB, attaching each
// memory's slot with a vector embedded from slotText(key, value). Returns the
// store, the dataset-id -> composite-id map, and a cleanup func.
func buildStore(ctx context.Context, cfg *config.Config, embedder *embed.OpenAIEmbedder, mems []memRecord, slotText func(key, value string) string, tag string) (*memory.Store, map[string]string, func()) {
	tmp, err := os.MkdirTemp("", "memslot-"+tag+"-*")
	must(err)
	database, err := db.Open(filepath.Join(tmp, "shared.db"), db.Shared, cfg.Embeddings.Dim)
	must(err)
	store := memory.NewStore(database, embedder)

	fmt.Printf("indexing [%s]", tag)
	start := time.Now()
	idMap := make(map[string]string, len(mems))
	for i, m := range mems {
		rec, err := store.Add(ctx, nil, types.CreateMemoryRequest{Scope: "shared", Content: m.Content, Source: "eval"}, 0)
		must(err)
		must(store.AddSlotEmbed(ctx, nil, rec.ID, m.Key, m.Value, slotText(m.Key, m.Value)))
		idMap[m.ID] = rec.ID
		if (i+1)%200 == 0 {
			fmt.Printf(" %d", i+1)
		}
	}
	fmt.Printf("  (%s)\n", time.Since(start).Round(time.Second))
	return store, idMap, func() { database.Close(); os.RemoveAll(tmp) }
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
