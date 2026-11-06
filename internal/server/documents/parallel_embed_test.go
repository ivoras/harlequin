package documents

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// lenEmbedder encodes each text's length into its vector, finishes batches out
// of order (earlier batches sleep longer) and tracks peak concurrency.
type lenEmbedder struct {
	inflight, peak atomic.Int32
	failOn         int32 // fail the Nth call (1-based); 0 = never
	calls          atomic.Int32
}

func (e *lenEmbedder) Embed(ctx context.Context, in []string) ([][]float32, error) {
	n := e.calls.Add(1)
	cur := e.inflight.Add(1)
	defer e.inflight.Add(-1)
	for {
		p := e.peak.Load()
		if cur <= p || e.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	time.Sleep(time.Duration(10-min(int(n), 9)) * 3 * time.Millisecond)
	if e.failOn == n {
		return nil, errors.New("boom")
	}
	out := make([][]float32, len(in))
	for i, s := range in {
		out[i] = []float32{float32(len(s)), 0, 0, 0}
	}
	return out, nil
}
func (e *lenEmbedder) EmbedQuery(ctx context.Context, in []string) ([][]float32, error) {
	return e.Embed(ctx, in)
}
func (e *lenEmbedder) Dim() int { return 4 }

func TestParallelEmbedKeepsOrderAndBoundsConcurrency(t *testing.T) {
	pdb, err := db.Open(filepath.Join(t.TempDir(), "project.db"), db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pdb.Close()
	// ~150 chunks of distinct lengths => several 32-chunk batches.
	var sb strings.Builder
	for i := 0; i < 150; i++ {
		sb.WriteString(strings.Repeat("w", 300+i) + ". ")
		sb.WriteString(strings.Repeat("v ", 300) + "\n\n")
	}
	emb := &lenEmbedder{}
	s := NewStore(nil, emb)
	s.SetEmbedConcurrency(3)
	var last atomic.Int32
	_, err = s.IngestIntoProgress(context.Background(), pdb, types.CreateDocumentRequest{Title: "t", Content: sb.String()}, 1,
		func(done, total int) {
			if int32(done) < last.Swap(int32(done)) {
				t.Errorf("progress went backwards to %d", done)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if p := emb.peak.Load(); p < 2 || p > 3 {
		t.Errorf("peak concurrency %d, want 2..3", p)
	}
	rows, err := pdb.Query(`SELECT c.content, v.embedding FROM doc_chunks c JOIN doc_chunks_vec v ON v.rowid = c.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var content string
		var blob []byte
		if err := rows.Scan(&content, &blob); err != nil {
			t.Fatal(err)
		}
		if got := math.Float32frombits(binary.LittleEndian.Uint32(blob)); int(got) != len(content) {
			t.Fatalf("chunk %d has another chunk's vector (%v vs len %d)", n, got, len(content))
		}
		n++
	}
	if n < 64 {
		t.Fatalf("only %d chunks indexed; test needs several batches", n)
	}

	// An embedding failure in any batch aborts the ingestion with that error.
	emb2 := &lenEmbedder{failOn: 2}
	s2 := NewStore(nil, emb2)
	s2.SetEmbedConcurrency(3)
	if _, err := s2.IngestInto(context.Background(), pdb, types.CreateDocumentRequest{Title: "t2", Content: sb.String()}, 1); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want embed error, got %v", err)
	}
}
