package documents

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// gateEmbedder blocks inside Embed until released, signalling when it is entered.
type gateEmbedder struct {
	entered chan struct{}
	release chan struct{}
}

func (g *gateEmbedder) Embed(ctx context.Context, in []string) ([][]float32, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	out := make([][]float32, len(in))
	for i := range out {
		out[i] = []float32{1, 0, 0, 0}
	}
	return out, nil
}
func (g *gateEmbedder) EmbedQuery(ctx context.Context, in []string) ([][]float32, error) {
	return g.Embed(ctx, in)
}
func (g *gateEmbedder) Dim() int { return 4 }

// An ingest must not hold SQLite's write lock while it waits on the embedding
// service: a second connection to the same file has to be able to write then
// (it used to block until busy_timeout and fail with "database is locked").
func TestIngestDoesNotHoldWriteLockWhileEmbedding(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "user.db")
	a, err := db.Open(path, db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := db.Open(path, db.Project, 4) // a second request's own connection
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	g := &gateEmbedder{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := NewStore(nil, g)
	done := make(chan error, 1)
	go func() {
		_, err := s.IngestInto(ctx, a, types.CreateDocumentRequest{
			Title: "Slow", Mime: "text/plain", Content: "Some text. " + strings.Repeat("More words here. ", 40),
		}, 1)
		done <- err
	}()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("embedder never called")
	}

	wrote := make(chan error, 1)
	go func() {
		_, err := b.ExecContext(ctx, `INSERT INTO documents(title, uri, mime) VALUES ('other', '', 'text/plain')`)
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("concurrent write failed while ingest was embedding: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent write blocked: ingest holds the write lock during embedding")
	}

	close(g.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var n int
	if err := a.QueryRow(`SELECT COUNT(*) FROM doc_chunks c JOIN documents d ON d.id = c.document_id WHERE d.title = 'Slow'`).Scan(&n); err != nil || n == 0 {
		t.Fatalf("chunks written: %d, %v", n, err)
	}
}
