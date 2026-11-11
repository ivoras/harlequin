package documents

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/db"
	"github.com/ivoras/harlequin/internal/shared/types"
)

type countEmbedder struct {
	fakeEmbedder
	texts int
}

func (c *countEmbedder) Embed(ctx context.Context, in []string) ([][]float32, error) {
	c.texts += len(in)
	return c.fakeEmbedder.Embed(ctx, in)
}

// filler returns n runes of non-repeating text, so no two windows are equal.
func filler(tag string, n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%s%d ", tag, i)
	}
	return b.String()[:n]
}

func replSetup(t *testing.T) (*Store, *countEmbedder, *sql.DB) {
	t.Helper()
	d, err := db.Open(":memory:", db.Project, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e := &countEmbedder{fakeEmbedder: fakeEmbedder{dim: 4}}
	return NewStore(nil, e), e, d
}

type chunkRow struct {
	id   int64
	ord  int
	page int
	text string
}

func rows(t *testing.T, d *sql.DB, docID int64) []chunkRow {
	t.Helper()
	rs, err := d.Query(`SELECT id, ord, page, content FROM doc_chunks WHERE document_id = ? ORDER BY ord`, docID)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []chunkRow
	for rs.Next() {
		var r chunkRow
		rs.Scan(&r.id, &r.ord, &r.page, &r.text)
		out = append(out, r)
	}
	return out
}

func ids(t *testing.T, d *sql.DB, q string, args ...any) []int64 {
	t.Helper()
	rs, err := d.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []int64
	for rs.Next() {
		var id int64
		rs.Scan(&id)
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// checkConsistent asserts ord is 0..n-1 and the three tables hold the same ids.
func checkConsistent(t *testing.T, d *sql.DB, docID int64) []chunkRow {
	t.Helper()
	rs := rows(t, d, docID)
	var want []int64
	for i, r := range rs {
		if r.ord != i {
			t.Fatalf("ord %d at position %d", r.ord, i)
		}
		want = append(want, r.id)
	}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	for _, tbl := range []string{"doc_chunks_fts", "doc_chunks_vec"} {
		got := ids(t, d, `SELECT rowid FROM `+tbl+` WHERE rowid IN (SELECT id FROM doc_chunks WHERE document_id = ?)`, docID)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s ids %v, want %v", tbl, got, want)
		}
	}
	// No orphans: every index row belongs to some chunk of any document.
	for _, tbl := range []string{"doc_chunks_fts", "doc_chunks_vec"} {
		if n := len(ids(t, d, `SELECT rowid FROM `+tbl+` WHERE rowid NOT IN (SELECT id FROM doc_chunks)`)); n != 0 {
			t.Fatalf("%s has %d orphans", tbl, n)
		}
	}
	return rs
}

func TestReplaceContentIncremental(t *testing.T) {
	ctx := context.Background()
	s, e, d := replSetup(t)
	oldText := filler("a", 2000) // windows at 0, 700, 1400
	doc, err := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "x", Content: oldText}, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := checkConsistent(t, d, doc.ID)

	// Identical content: nothing embedded, nothing renumbered.
	e.texts = 0
	if n, err := s.ReplaceContent(ctx, d, doc.ID, oldText); err != nil || n != len(before) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if e.texts != 0 {
		t.Fatalf("identical content embedded %d chunks", e.texts)
	}
	if !reflect.DeepEqual(before, checkConsistent(t, d, doc.ID)) {
		t.Fatal("identical content changed chunk rows")
	}

	// Append: the first two windows are untouched; the old tail changes.
	newText := oldText + filler("zz", 1500)
	e.texts = 0
	n, err := s.ReplaceContent(ctx, d, doc.ID, newText)
	if err != nil {
		t.Fatal(err)
	}
	after := checkConsistent(t, d, doc.ID)
	if n != len(after) || len(after) <= len(before) {
		t.Fatalf("n=%d chunks=%d before=%d", n, len(after), len(before))
	}
	if e.texts != len(after)-2 {
		t.Fatalf("embedded %d, want %d", e.texts, len(after)-2)
	}
	for i := 0; i < 2; i++ {
		if after[i].id != before[i].id {
			t.Fatalf("chunk %d id changed", i)
		}
	}
	for _, r := range after[2:] {
		if r.id == before[2].id {
			t.Fatal("removed chunk survived")
		}
	}
	if hits, _ := s.FindText(ctx, d, doc.ID, "zz3 ", 5); len(hits) == 0 {
		t.Fatal("added text not found")
	}
	if got, _ := s.FullText(ctx, d, doc.ID); got != mustFull(t, s, d, newText) {
		t.Fatal("FullText differs from a fresh ingest")
	}

	// Shrink back: the added text disappears.
	if _, err := s.ReplaceContent(ctx, d, doc.ID, oldText); err != nil {
		t.Fatal(err)
	}
	checkConsistent(t, d, doc.ID)
	if hits, _ := s.FindText(ctx, d, doc.ID, "zz3 ", 5); len(hits) != 0 {
		t.Fatal("removed text still found")
	}
}

func mustFull(t *testing.T, s *Store, d *sql.DB, text string) string {
	t.Helper()
	doc, err := s.IngestInto(context.Background(), d, types.CreateDocumentRequest{Title: "fresh", Content: text}, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.FullText(context.Background(), d, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplaceContentDuplicateChunks(t *testing.T) {
	ctx := context.Background()
	s, e, d := replSetup(t)
	// Period-700 text: every window (start multiple of 700) is identical.
	unit := filler("r", 700)
	oldText := strings.Repeat(unit, 4) // windows 0..2100 of 800 runes
	doc, _ := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "x", Content: oldText}, 1)
	before := checkConsistent(t, d, doc.ID)
	e.texts = 0
	if _, err := s.ReplaceContent(ctx, d, doc.ID, oldText); err != nil || e.texts != 0 {
		t.Fatalf("err=%v embedded=%d", err, e.texts)
	}
	after := checkConsistent(t, d, doc.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rows changed")
	}
	// One more repetition: existing duplicates are reused one-to-one, never twice.
	if _, err := s.ReplaceContent(ctx, d, doc.ID, oldText+unit); err != nil {
		t.Fatal(err)
	}
	got := checkConsistent(t, d, doc.ID)
	seen := map[int64]bool{}
	for _, r := range got {
		if seen[r.id] {
			t.Fatalf("id %d used twice", r.id)
		}
		seen[r.id] = true
	}
	for i := 0; i < len(before)-1; i++ {
		if got[i].id != before[i].id {
			t.Fatalf("chunk %d not reused in order", i)
		}
	}
}

func TestReplaceContentPages(t *testing.T) {
	ctx := context.Background()
	s, _, d := replSetup(t)
	text := filler("p", 2800)      // windows at 0,700,1400,2100
	starts := []int{0, 1000, 2000} // pages 1,2,3
	doc, _ := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "x", Content: text, PageStarts: starts}, 1)
	before := checkConsistent(t, d, doc.ID)
	if before[1].page != 1 || before[2].page != 2 || before[3].page != 3 {
		t.Fatalf("setup pages %+v", before)
	}
	// Append without page starts: kept chunks keep pages, new one takes its neighbour's.
	if _, err := s.ReplaceContent(ctx, d, doc.ID, text+filler("q", 1500)); err != nil {
		t.Fatal(err)
	}
	after := checkConsistent(t, d, doc.ID)
	for i := 0; i < 3; i++ {
		if after[i].id != before[i].id || after[i].page != before[i].page {
			t.Fatalf("chunk %d: %+v vs %+v", i, after[i], before[i])
		}
	}
	for _, r := range after[3:] {
		if r.page != 2 {
			t.Fatalf("new chunk page %d, want 2 (preceding chunk's)", r.page)
		}
	}
	// Page starts supplied: everything recomputed.
	if _, err := s.ReplaceContentPaged(ctx, d, doc.ID, text+filler("q", 1500), []int{0, 700}); err != nil {
		t.Fatal(err)
	}
	for i, r := range checkConsistent(t, d, doc.ID) {
		want := 1
		if i >= 1 {
			want = 2
		}
		if r.page != want {
			t.Fatalf("chunk %d page %d, want %d", i, r.page, want)
		}
	}
}

func TestReplaceContentInsertAtStart(t *testing.T) {
	ctx := context.Background()
	s, _, d := replSetup(t)
	text := filler("s", 4000)
	doc, _ := s.IngestInto(ctx, d, types.CreateDocumentRequest{Title: "x", Content: text}, 1)
	newText := "Inserted paragraph.\n\n" + text
	if _, err := s.ReplaceContent(ctx, d, doc.ID, newText); err != nil {
		t.Fatal(err)
	}
	checkConsistent(t, d, doc.ID)
	if got, _ := s.FullText(ctx, d, doc.ID); got != mustFull(t, s, d, newText) {
		t.Fatal("FullText differs from a fresh ingest")
	}
}
