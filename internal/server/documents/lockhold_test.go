package documents

import (
	"context"
	"fmt"
	"os"
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

func bigText(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "Paragraph %d states a distinct obligation of the tenant number %d. It continues for a while with more words to fill a chunk. ", i, i*7)
		if i%3 == 0 {
			sb.WriteString("\n\n")
		}
	}
	return sb.String()
}

// Deleting a large document must not hold the write lock for its whole
// duration: a concurrent writer waits for at most one small batch.
func TestDeleteFromLeavesWriteLockFree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "p.db")
	a, err := db.Open(path, db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := db.Open(path, db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	s := NewStore(nil, fakeEmbedder{dim: 4})
	doc, err := s.IngestInto(ctx, a, types.CreateDocumentRequest{Title: "Big", Mime: "text/plain", Content: bigText(9000)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Chunks < 1500 {
		t.Fatalf("test needs a big document, got %d chunks", doc.Chunks)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- s.DeleteFrom(ctx, a, "", doc.ID) }()
	var worst time.Duration
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
			t0 := time.Now()
			if _, err := b.ExecContext(ctx, `INSERT INTO documents(title, uri, mime) VALUES ('w', '', 'text/plain')`); err != nil {
				t.Fatalf("concurrent write failed during delete: %v", err)
			}
			worst = max(worst, time.Since(t0))
			time.Sleep(time.Millisecond)
		}
	}
	total := time.Since(start)
	t.Logf("%d chunks; delete took %v; worst concurrent write wait %v", doc.Chunks, total, worst)
	// A single big transaction would make the writer wait for nearly all of it.
	if worst > total/3 {
		t.Fatalf("a concurrent write waited %v of a %v delete: the lock is held too long", worst, total)
	}
	var n int
	a.QueryRow(`SELECT (SELECT COUNT(*) FROM doc_chunks WHERE document_id = ?) + (SELECT COUNT(*) FROM documents WHERE id = ?)`, doc.ID, doc.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("document not fully deleted: %d rows left", n)
	}
}

// If the stored file cannot be written after the copy committed, the copy is
// undone: a move must never delete the original while the target is incomplete.
func TestCopyIntoUndoesCopyWhenFileWriteFails(t *testing.T) {
	ctx := context.Background()
	src, _ := db.Open(":memory:", db.Project, 4)
	dst, _ := db.Open(":memory:", db.Project, 4)
	defer src.Close()
	defer dst.Close()
	s := NewStore(nil, fakeEmbedder{dim: 4})
	doc, err := s.IngestInto(ctx, src, types.CreateDocumentRequest{Title: "C.pdf", Mime: "application/pdf", Content: "Some contract text. " + strings.Repeat("More. ", 50)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	srcFiles := t.TempDir()
	os.WriteFile(filepath.Join(srcFiles, "1-C.pdf"), []byte("%PDF"), 0o600)
	src.Exec(`UPDATE documents SET stored_path = '1-C.pdf' WHERE id = ?`, doc.ID)

	_, err = s.CopyInto(ctx, src, dst, srcFiles, filepath.Join(t.TempDir(), "missing-dir"), doc.ID)
	if err == nil {
		t.Fatal("expected the failed file write to fail the copy")
	}
	var docs, chunks int
	dst.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&docs)
	dst.QueryRow(`SELECT COUNT(*) FROM doc_chunks`).Scan(&chunks)
	if docs != 0 || chunks != 0 {
		t.Fatalf("failed copy left %d documents and %d chunks behind", docs, chunks)
	}
	if _, err := src.Exec(`SELECT 1 FROM documents WHERE id = ?`, doc.ID); err != nil {
		t.Fatal("source must be untouched")
	}
}
