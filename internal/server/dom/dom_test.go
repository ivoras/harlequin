package dom

import (
	"strings"
	"testing"
)

const sample = `<!doctype html><html><head><title>Calls</title></head>
<body>
  <div id="main">
    <h1>Public calls</h1>
    <ul class="calls">
      <li class="call"><a href="/call/1">Solar panel subsidy 2024</a></li>
      <li class="call"><a href="/call/2">EV charger grant</a></li>
      <li class="call"><a href="/call/3">Heat pump <b>rebate</b> programme</a></li>
    </ul>
  </div>
</body></html>`

func mustParse(t *testing.T) *Doc {
	t.Helper()
	d, err := Parse([]byte(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return d
}

func TestQuery(t *testing.T) {
	d := mustParse(t)
	nodes, err := d.Query("ul.calls > li.call", 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}
	if nodes[0].Tag != "li" || nodes[0].Class != "call" {
		t.Errorf("node[0] = %+v", nodes[0])
	}
	if !strings.Contains(nodes[0].Text, "Solar panel subsidy") {
		t.Errorf("node[0].Text = %q", nodes[0].Text)
	}
}

// The path a query/grep reports must select that same node when fed back in —
// this is what lets a discovered path become a saved, LLM-free parser.
func TestPathRoundTrips(t *testing.T) {
	d := mustParse(t)
	nodes, err := d.Query("li.call > a", 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d, want 3", len(nodes))
	}
	for i, n := range nodes {
		if n.Path == "" {
			t.Fatalf("node[%d] has empty path", i)
		}
		back, err := d.Query(n.Path, 0)
		if err != nil {
			t.Fatalf("re-query %q: %v", n.Path, err)
		}
		if len(back) != 1 {
			t.Fatalf("path %q matched %d nodes, want exactly 1", n.Path, len(back))
		}
		if back[0].Text != n.Text {
			t.Errorf("path %q round-trip text mismatch: %q vs %q", n.Path, back[0].Text, n.Text)
		}
	}
}

func TestGrepDeepest(t *testing.T) {
	d := mustParse(t)
	// "rebate" lives inside <b> within an <a>; the deepest text wrapper is the <b>.
	hits, err := d.Grep("rebate", GrepOptions{IgnoreCase: true})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Tag != "b" {
		t.Errorf("deepest tag = %q, want b", hits[0].Tag)
	}

	// Text split across inline children: the deepest wrapper is the <a>.
	hits, err = d.Grep("Heat pump rebate programme", GrepOptions{IgnoreCase: true})
	if err != nil {
		t.Fatalf("Grep split: %v", err)
	}
	if len(hits) != 1 || hits[0].Tag != "a" {
		t.Fatalf("split-text grep = %+v, want one <a>", hits)
	}
}

func TestGrepAttrs(t *testing.T) {
	d := mustParse(t)
	hits, err := d.Grep("/call/2", GrepOptions{Attrs: true})
	if err != nil {
		t.Fatalf("Grep attrs: %v", err)
	}
	if len(hits) != 1 || hits[0].Tag != "a" || hits[0].Attrs["href"] != "/call/2" {
		t.Fatalf("attr grep = %+v", hits)
	}
}

func TestRepeatingGroups(t *testing.T) {
	html := `<html><body>
	<ul class="nav"><li>Home</li><li>About</li></ul>
	<ul class="calls">
	  <li class="accordion-header accordion-header-natjecaji"><div>otvoren</div><div>Call One</div></li>
	  <li class="accordion-header accordion-header-natjecaji"><div>otvoren</div><div>Call Two</div></li>
	  <li class="accordion-header accordion-header-natjecaji"><div>zatvoren</div><div>Call Three</div></li>
	</ul></body></html>`
	d, err := Parse([]byte(html))
	if err != nil {
		t.Fatal(err)
	}
	groups := d.RepeatingGroups(3, 10, 80)
	var found *GroupCandidate
	for i := range groups {
		if strings.Contains(groups[i].Selector, "accordion-header-natjecaji") {
			found = &groups[i]
		}
	}
	if found == nil {
		t.Fatalf("expected the calls list group, got %+v", groups)
	}
	if found.Count != 3 {
		t.Errorf("count = %d, want 3", found.Count)
	}
	// The 2-item nav list is below minCount and must be excluded.
	for _, g := range groups {
		if g.Count < 3 {
			t.Errorf("group below minCount leaked: %+v", g)
		}
	}
	// The reported selector must actually select the items.
	nodes, err := d.Query(found.Selector, 0)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("selector %q -> %d nodes (err %v), want 3", found.Selector, len(nodes), err)
	}
}

// GrepFlatten matches each flattened line against the pattern as a
// case-insensitive regexp. The motivating bug: a caller passing an alternation
// like "price|€|\\$" got nothing back because the pattern was matched as a
// literal substring.
func TestGrepFlattenRegex(t *testing.T) {
	const shop = `<html><body>
	  <div class="product"><span class="micro-price">148500</span></div>
	  <div class="price"><span class="oct-price-new">¥148 500</span></div>
	</body></html>`
	d, err := Parse([]byte(shop))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Alternation must match: "price" appears (in a class descriptor) and "¥..."
	// appears as text — but neither "€" nor "$" is present.
	res := d.GrepFlatten(`price|€|\$`, 0, nil)
	if res == "" {
		t.Fatal("alternation matched nothing; regex grep is broken")
	}
	if !strings.Contains(res, "148 500") {
		t.Errorf("expected the price line in results, got:\n%s", res)
	}

	// Case-insensitive: uppercase pattern still matches lowercase content.
	if d.GrepFlatten("PRICE", 0, nil) == "" {
		t.Error("grep should be case-insensitive")
	}

	// A pattern matching nothing returns empty (not an error string).
	if got := d.GrepFlatten("nonexistentxyz", 0, nil); got != "" {
		t.Errorf("no-match should return empty, got %q", got)
	}

	// An invalid regexp must fall back to a literal substring match rather than
	// erroring out the whole search. The trailing ")" is an unbalanced group, so
	// this fails to compile; as a literal it appears in the descriptor line
	// span(class="micro-price").
	if d.GrepFlatten(`micro-price")`, 0, nil) == "" {
		t.Error("invalid regexp should fall back to literal substring match")
	}
}

func TestSkeletonDepth(t *testing.T) {
	d := mustParse(t)
	sk, err := d.Skeleton(SkelOptions{Selector: "ul.calls", MaxDepth: 1, Paths: true})
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}
	if len(sk) != 1 || sk[0].Tag != "ul" {
		t.Fatalf("root = %+v", sk)
	}
	if len(sk[0].Children) != 3 {
		t.Fatalf("got %d children, want 3", len(sk[0].Children))
	}
	// MaxDepth 1 means children's children are omitted.
	if len(sk[0].Children[0].Children) != 0 {
		t.Errorf("depth limit not honored: %+v", sk[0].Children[0])
	}
	if sk[0].Path == "" {
		t.Error("Paths=true should set Path")
	}
}
