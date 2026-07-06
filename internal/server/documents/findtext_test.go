package documents

import (
	"context"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/server/embed"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type fakeEmbedder struct{ dim int }

func (f fakeEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range out {
		out[i] = make([]float32, f.dim)
	}
	return out, nil
}
func (f fakeEmbedder) EmbedQuery(ctx context.Context, inputs []string) ([][]float32, error) {
	return f.Embed(ctx, inputs)
}
func (f fakeEmbedder) Dim() int { return f.dim }

var _ embed.Embedder = fakeEmbedder{}

func TestFindText(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := db.Open(":memory:", db.Shared, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	store := NewStore(sqlDB, fakeEmbedder{dim: 4})

	doc, err := store.Ingest(ctx, types.CreateDocumentRequest{
		Title:   "Test regulation",
		Content: "Article 4.4 Cooperation Committee shall be established.\n\nArticle 4.5 Donor partnership projects.",
	}, 1)
	if err != nil {
		t.Fatal(err)
	}

	hits, err := store.FindText(ctx, sqlDB, doc.ID, "Cooperation Committee", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
	if hits[0].ChunkID == 0 {
		t.Fatal("hit should carry a real chunk id")
	}

	// Case-insensitive.
	hitsLower, err := store.FindText(ctx, sqlDB, doc.ID, "cooperation committee", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hitsLower) != 1 {
		t.Fatalf("want 1 case-insensitive hit, got %d", len(hitsLower))
	}

	none, err := store.FindText(ctx, sqlDB, doc.ID, "Fund for Civil Society", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("want no hits for absent text, got %d", len(none))
	}
}
