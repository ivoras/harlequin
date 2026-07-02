// Command memrrfsweep sweeps memory-search FTS/vector RRF weights on live data.
//
//	go run ./cmd/memrrfsweep --config server.yaml --user 3 --q woodchuck
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/server/memory"
)

func main() {
	cfgPath := flag.String("config", "server.yaml", "server config")
	userID := flag.Int64("user", 3, "user id")
	query := flag.String("q", "woodchuck", "search query")
	limit := flag.Int("limit", 6, "results per search")
	slotWeight := flag.Float64("slot", 0, "slot-key RRF weight (0 disables for FTS/vector isolation)")
	sweep := flag.String("sweep", "fts", "which weight to sweep: fts or vector")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	shared, err := db.Open(filepath.Join(cfg.DataDir, "shared.db"), db.Shared, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatal(err)
	}
	defer shared.Close()
	userDB, err := db.Open(filepath.Join(cfg.DataDir, "users", fmt.Sprintf("%d", *userID), "user.db"), db.User, cfg.Embeddings.Dim)
	if err != nil {
		log.Fatal(err)
	}
	defer userDB.Close()

	e := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim, cfg.Embeddings.QueryPrefix, cfg.Embeddings.DocPrefix)
	store := memory.NewStore(shared, e)
	store.SetSearchMaxDistance(cfg.Memory.SearchMaxDistanceValue())
	store.SetSlotSearchWeight(*slotWeight)

	weights := []float64{0, 0.25, 0.5, 1, 1.5, 2, 3, 4, 8}
	ctx := context.Background()
	baseFTS := cfg.Memory.FTSWeightValue()
	baseVec := cfg.Memory.VectorWeightValue()

	fmt.Printf("query=%q user=%d limit=%d slot_weight=%.1f search_max_distance=%.2f\n\n",
		*query, *userID, *limit, *slotWeight, cfg.Memory.SearchMaxDistanceValue())

	for _, w := range weights {
		ftsW, vecW := baseFTS, baseVec
		switch *sweep {
		case "fts":
			ftsW = w
		case "vector":
			vecW = w
		default:
			log.Fatalf("unknown -sweep %q (use fts or vector)", *sweep)
		}
		store.SetFusion(ftsW, vecW)
		res, err := store.Search(ctx, userDB, *query, *userID, "", *limit)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("fts_weight=%-5.2f vector_weight=%-5.2f\n", ftsW, vecW)
		if len(res) == 0 {
			fmt.Println("  (no results)")
			continue
		}
		for i, r := range res {
			mark := ""
			if isWoodchuckHit(r.ID, r.Content) {
				mark = " *"
			}
			fmt.Printf("  %d. [%s] %s%s\n", i+1, r.ID, truncate(r.Content, 70), mark)
		}
		fmt.Println()
	}
}

func isWoodchuckHit(id, content string) bool {
	known := map[string]bool{"s.8": true, "s.9": true, "u.18": true, "u.34": true}
	if known[id] {
		return true
	}
	return strings.Contains(strings.ToLower(content), "woodchuck")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
