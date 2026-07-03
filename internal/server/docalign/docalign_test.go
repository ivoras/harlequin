package docalign

import (
	"testing"
)

// vec builds a tiny fake embedding; texts get similar vectors when their seed
// values are close.
func vec(x, y float64) []float32 {
	return []float32{float32(x), float32(y)}
}

func sec(ord int, text string, v []float32) Section {
	return Section{ChunkID: int64(ord + 1), Ord: ord, Text: text, Vec: v}
}

func doc(title string, secs ...Section) *Doc {
	return &Doc{ID: 1, Title: title, Scope: "shared", Sections: secs}
}

func kinds(r *Result) []Kind {
	out := make([]Kind, len(r.Pairs))
	for i, p := range r.Pairs {
		out[i] = p.Kind
	}
	return out
}

func TestAlignVersionsIdenticalDocs(t *testing.T) {
	a := doc("a", sec(0, "one", vec(1, 0)), sec(1, "two", vec(0, 1)), sec(2, "three", vec(1, 1)))
	b := doc("b", sec(0, "one", vec(1, 0)), sec(1, "two", vec(0, 1)), sec(2, "three", vec(1, 1)))
	r := AlignVersions(a, b, 0.55)
	if r.Identical != 3 || len(r.Pairs) != 0 {
		t.Fatalf("identical docs: got identical=%d pairs=%v", r.Identical, kinds(r))
	}
}

func TestAlignVersionsWhitespaceIsIdentical(t *testing.T) {
	a := doc("a", sec(0, "one  two\nthree", nil))
	b := doc("b", sec(0, "one two three", nil))
	r := AlignVersions(a, b, 0.55)
	if r.Identical != 1 || len(r.Pairs) != 0 {
		t.Fatalf("reflowed text should be identical: identical=%d pairs=%v", r.Identical, kinds(r))
	}
}

func TestAlignVersionsChangeAddRemove(t *testing.T) {
	// a: intro, penalty-old, tail, removed
	// b: intro, penalty-new, added, tail
	a := doc("a",
		sec(0, "intro", vec(1, 0)),
		sec(1, "penalty is 100 EUR", vec(0.1, 1)),
		sec(2, "tail", vec(-1, 0.2)),
		sec(3, "removed clause", vec(0.5, -1)),
	)
	b := doc("b",
		sec(0, "intro", vec(1, 0)),
		sec(1, "penalty is 500 EUR", vec(0.12, 1)), // near penalty-old
		sec(2, "brand new clause", vec(1, -0.9)),   // far from everything in the gap
		sec(3, "tail", vec(-1, 0.2)),
	)
	r := AlignVersions(a, b, 0.55)
	if r.Identical != 2 {
		t.Fatalf("want 2 identical anchors (intro, tail), got %d", r.Identical)
	}
	got := kinds(r)
	want := map[Kind]int{Changed: 1, OnlyA: 1, OnlyB: 1}
	if len(got) != 3 || r.Counts()[Changed] != want[Changed] ||
		r.Counts()[OnlyA] != want[OnlyA] || r.Counts()[OnlyB] != want[OnlyB] {
		t.Fatalf("want one changed + one only_a + one only_b, got %v", got)
	}
	for _, p := range r.Pairs {
		if p.Kind == Changed && (p.A[0].Text != "penalty is 100 EUR" || p.B[0].Text != "penalty is 500 EUR") {
			t.Fatalf("changed pair mismatched: %q vs %q", p.A[0].Text, p.B[0].Text)
		}
	}
}

func TestAlignVersionsNoVectorsFallsBackSequential(t *testing.T) {
	a := doc("a", sec(0, "same", nil), sec(1, "old text", nil), sec(2, "extra a", nil))
	b := doc("b", sec(0, "same", nil), sec(1, "new text", nil))
	r := AlignVersions(a, b, 0.55)
	if r.Identical != 1 {
		t.Fatalf("want 1 identical, got %d", r.Identical)
	}
	got := kinds(r)
	if len(got) != 2 || got[0] != Changed || got[1] != OnlyA {
		t.Fatalf("want [changed only_a], got %v", got)
	}
}

func TestAlignTopicalMatchAndOrphans(t *testing.T) {
	a := doc("law A",
		sec(0, "data retention rules", vec(1, 0)),
		sec(1, "penalties", vec(0, 1)),
		sec(2, "topic only in A", vec(-1, -1)),
	)
	b := doc("law B",
		sec(0, "sanctions and fines", vec(0.05, 1)), // ~ penalties
		sec(1, "retention of records", vec(1, 0.1)), // ~ data retention
		sec(2, "topic only in B", vec(-1, 1)),
	)
	r, err := AlignTopical(a, b, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Counts()
	if c[Matched] != 2 || c[OnlyA] != 1 || c[OnlyB] != 1 {
		t.Fatalf("want 2 matched + 1 only_a + 1 only_b, got %v", kinds(r))
	}
	// Matched pairs must cross-link by topic, not by position.
	for _, p := range r.Pairs {
		if p.Kind != Matched {
			continue
		}
		switch p.A[0].Ord {
		case 0:
			if p.B[0].Ord != 1 {
				t.Fatalf("retention matched wrong section: B ord %d", p.B[0].Ord)
			}
		case 1:
			if p.B[0].Ord != 0 {
				t.Fatalf("penalties matched wrong section: B ord %d", p.B[0].Ord)
			}
		}
	}
}

func TestAlignTopicalMissingVectorsErrors(t *testing.T) {
	a := doc("a", sec(0, "x", nil))
	b := doc("b", sec(0, "y", vec(1, 0)))
	if _, err := AlignTopical(a, b, 0.5); err == nil {
		t.Fatal("want error for missing embeddings")
	}
}

func TestGroupRunSplitsLongRuns(t *testing.T) {
	run := make([]Section, 7)
	for i := range run {
		run[i] = sec(i, "s", nil)
	}
	pairs := groupRun(run, OnlyB)
	if len(pairs) != 3 || len(pairs[0].B) != 3 || len(pairs[2].B) != 1 {
		t.Fatalf("want groups of 3/3/1, got %d groups", len(pairs))
	}
}

func TestPatienceRepeatedSectionsViaLCS(t *testing.T) {
	// "x" repeats, so it is never a unique anchor; the small-range LCS must
	// still match both occurrences.
	ak := []string{"x", "mid a", "x"}
	bk := []string{"x", "mid b", "x"}
	var out [][2]int
	patience(ak, bk, 0, 3, 0, 3, &out)
	if len(out) != 2 || out[0] != [2]int{0, 0} || out[1] != [2]int{2, 2} {
		t.Fatalf("want [(0,0) (2,2)], got %v", out)
	}
}

func TestDecodeVec(t *testing.T) {
	blob := []byte{0, 0, 128, 63, 0, 0, 0, 64} // 1.0, 2.0 little-endian
	v := decodeVec(blob)
	if len(v) != 2 || v[0] != 1 || v[1] != 2 {
		t.Fatalf("got %v", v)
	}
	if decodeVec([]byte{1, 2, 3}) != nil {
		t.Fatal("misaligned blob should decode to nil")
	}
}
