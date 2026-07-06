package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/docalign"
)

func TestParsePairSpec(t *testing.T) {
	cases := []struct {
		spec    string
		maxPair int
		want    []int
		wantErr bool
	}{
		{"9,12,16", 20, []int{9, 12, 16}, false},
		{"19-22", 30, []int{19, 20, 21, 22}, false},
		{"9,12,16-19,44", 50, []int{9, 12, 16, 17, 18, 19, 44}, false},
		{"5,5,5", 10, []int{5}, false},       // dedup
		{"3,1,2", 10, []int{1, 2, 3}, false}, // sorted
		{" 3 , 1 ", 10, []int{1, 3}, false},  // whitespace tolerant
		{"", 10, nil, true},                  // empty
		{"0", 10, nil, true},                 // out of range low
		{"11", 10, nil, true},                // out of range high
		{"3-", 10, nil, true},                // malformed range
		{"abc", 10, nil, true},               // not a number
		{"1-100000", 200000, nil, true},      // range too large for one call
	}
	for _, c := range cases {
		got, errMsg := parsePairSpec(c.spec, c.maxPair, alignMaxSelectable)
		if c.wantErr {
			if errMsg == "" {
				t.Errorf("parsePairSpec(%q): want error, got %v", c.spec, got)
			}
			continue
		}
		if errMsg != "" {
			t.Errorf("parsePairSpec(%q): unexpected error %q", c.spec, errMsg)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePairSpec(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestParsePairSpecTooManySelected(t *testing.T) {
	spec := "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59,60,61"
	if _, errMsg := parsePairSpec(spec, 200, alignMaxSelectable); errMsg == "" {
		t.Fatal("want error when more than alignMaxSelectable pairs are named")
	}
}

func TestUnitPairMatchesORTerms(t *testing.T) {
	p := docalign.UnitPair{
		Kind: docalign.Changed,
		A:    &docalign.Unit{Heading: "Article 6.4 Grant rates and size of project grants"},
		B:    &docalign.Unit{Heading: "Article 6.4 Grant rates and size of project grants"},
	}
	if !unitPairMatches(p, strings.ToLower("subsidy,grant,funding")) {
		t.Fatal("want match: one OR term (grant) is present")
	}
	if unitPairMatches(p, strings.ToLower("subsidy,audit")) {
		t.Fatal("want no match: neither OR term present")
	}
}

func TestRenderUnitPairsExplicitOrderAndContinuation(t *testing.T) {
	docA := &docalign.Doc{ID: 1, Title: "old", Scope: "project"}
	docB := &docalign.Doc{ID: 2, Title: "new", Scope: "project"}
	mkUnit := func(n int) *docalign.Unit {
		return &docalign.Unit{
			Heading: "Article " + string(rune('A'+n)),
			Secs:    []docalign.Section{{ChunkID: int64(n), Text: "text " + string(rune('A'+n))}},
		}
	}
	res := &docalign.UnitResult{}
	for i := 1; i <= 5; i++ {
		res.Pairs = append(res.Pairs, docalign.UnitPair{Kind: docalign.Changed, A: mkUnit(i), B: mkUnit(i)})
	}
	ag := &Agent{}
	out := ag.renderUnitPairsExplicit(context.Background(), docA, nil, docB, nil, res, []int{2, 4, 5})
	if !strings.Contains(out, "Pair #2") || !strings.Contains(out, "Pair #4") || !strings.Contains(out, "Pair #5") {
		t.Fatalf("expected pairs 2, 4, 5 rendered, got:\n%s", out)
	}
	if strings.Contains(out, "Pair #1") || strings.Contains(out, "Pair #3") {
		t.Fatalf("expected only the requested pairs, got:\n%s", out)
	}
	if !strings.Contains(out, "Showing 3 of 3 requested") {
		t.Fatalf("expected completion footer, got:\n%s", out)
	}
}

func TestRenderUnitPairsExplicitBatchesLargeSelection(t *testing.T) {
	docA := &docalign.Doc{ID: 1, Title: "old", Scope: "project"}
	docB := &docalign.Doc{ID: 2, Title: "new", Scope: "project"}
	res := &docalign.UnitResult{}
	for i := 1; i <= 20; i++ {
		u := &docalign.Unit{Heading: "H", Secs: []docalign.Section{{ChunkID: int64(i), Text: "short text"}}}
		res.Pairs = append(res.Pairs, docalign.UnitPair{Kind: docalign.Changed, A: u, B: u})
	}
	nums := make([]int, 20)
	for i := range nums {
		nums[i] = i + 1
	}
	ag := &Agent{}
	out := ag.renderUnitPairsExplicit(context.Background(), docA, nil, docB, nil, res, nums)
	if !strings.Contains(out, "call align_docs again with view=\"pairs\" and pairs=") {
		t.Fatalf("expected a continuation footer for a selection larger than one batch, got:\n%s", out)
	}
}
