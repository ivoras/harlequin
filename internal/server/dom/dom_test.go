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
