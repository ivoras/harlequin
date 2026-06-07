// Package dom parses HTML and locates content within it: CSS-selector queries,
// text/attribute grep, and a compact structural JSON skeleton. Every located node
// carries a stable CSS Path that Query accepts, so a path discovered once (e.g.
// by the WebFetchDOM tool, with the help of the LLM) can be replayed verbatim by a
// saved parser — letting periodic checks extract data with no LLM in the loop.
//
// It is a pure package (no network, no I/O) so it can back both the WebFetchDOM
// agent tool and the JavaScript sandbox.
package dom

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// defaultTextChars bounds the text snippet attached to a located node.
const defaultTextChars = 240

// Doc is a parsed HTML document.
type Doc struct {
	doc *goquery.Document
}

// Node is a located element: a stable CSS Path plus a compact summary the small
// model can read cheaply.
type Node struct {
	Path  string            `json:"path"`
	Tag   string            `json:"tag"`
	ID    string            `json:"id,omitempty"`
	Class string            `json:"class,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
	Text  string            `json:"text,omitempty"`
}

// Parse parses an HTML document.
func Parse(htmlBytes []byte) (*Doc, error) {
	d, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil, err
	}
	return &Doc{doc: d}, nil
}

// root returns the underlying document html.Node.
func (d *Doc) root() *html.Node {
	if len(d.doc.Nodes) == 0 {
		return nil
	}
	return d.doc.Nodes[0]
}

// Query returns the elements matching a CSS selector, each with its stable path.
// textChars bounds each result's text snippet (<=0 uses the default).
func (d *Doc) Query(selector string, textChars int) ([]Node, error) {
	m, err := cascadia.Compile(strings.TrimSpace(selector))
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	if textChars <= 0 {
		textChars = defaultTextChars
	}
	var out []Node
	d.doc.FindMatcher(m).Each(func(_ int, s *goquery.Selection) {
		if len(s.Nodes) > 0 {
			out = append(out, summary(s.Nodes[0], textChars))
		}
	})
	return out, nil
}

// RootNode returns the document's root node, the default context for scoped
// node queries.
func (d *Doc) RootNode() *html.Node { return d.root() }

// QueryNode returns the element nodes matching selector within ctx's subtree.
// It backs the JS sandbox's chainable dom.query (query results are themselves
// queryable contexts).
func QueryNode(ctx *html.Node, selector string) ([]*html.Node, error) {
	m, err := cascadia.Compile(strings.TrimSpace(selector))
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	return cascadia.QueryAll(ctx, m), nil
}

// Summarize builds a Node summary (stable path + compact metadata + text) for an
// element node.
func Summarize(n *html.Node, textChars int) Node {
	if textChars <= 0 {
		textChars = defaultTextChars
	}
	return summary(n, textChars)
}

// GrepOptions tunes Grep.
type GrepOptions struct {
	// Regex treats the pattern as a regular expression instead of a substring.
	Regex bool
	// IgnoreCase makes matching case-insensitive (default: caller passes true).
	IgnoreCase bool
	// Attrs also matches attribute values (e.g. href), not just text.
	Attrs bool
	// MaxMatches caps the number of returned nodes (0 = unlimited).
	MaxMatches int
	// TextChars bounds each result's text snippet (<=0 uses the default).
	TextChars int
}

// Grep finds the most specific elements whose text (or, with Attrs, an attribute
// value) matches pattern. For text matches it returns the deepest wrapping
// element — the one that contains the match but whose element children do not —
// so the path points precisely at the datum rather than at a huge ancestor.
func (d *Doc) Grep(pattern string, opts GrepOptions) ([]Node, error) {
	return GrepNode(d.root(), pattern, opts)
}

// GrepNode runs Grep within ctx's subtree (ctx may be any element node).
func GrepNode(ctx *html.Node, pattern string, opts GrepOptions) ([]Node, error) {
	match, err := matcherFor(pattern, opts)
	if err != nil {
		return nil, err
	}
	root := ctx
	if root == nil {
		return nil, nil
	}
	order := docOrder(root)
	seen := map[*html.Node]bool{}
	var hits []*html.Node
	add := func(n *html.Node) {
		if !seen[n] {
			seen[n] = true
			hits = append(hits, n)
		}
	}

	// Text matches: descend only along branches whose subtree text matches, and
	// emit the deepest such element on each branch.
	var collect func(n *html.Node)
	collect = func(n *html.Node) {
		deeper := false
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && match(fullText(c)) {
				deeper = true
				collect(c)
			}
		}
		if !deeper {
			add(n)
		}
	}
	var seed func(n *html.Node)
	seed = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if match(fullText(c)) {
				collect(c)
			} else {
				seed(c)
			}
		}
	}
	seed(root)

	// Attribute matches (the element itself is the most specific node).
	if opts.Attrs {
		var attrWalk func(n *html.Node)
		attrWalk = func(n *html.Node) {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.ElementNode {
					continue
				}
				for _, a := range c.Attr {
					if match(a.Val) {
						add(c)
						break
					}
				}
				attrWalk(c)
			}
		}
		attrWalk(root)
	}

	sort.Slice(hits, func(i, j int) bool { return order[hits[i]] < order[hits[j]] })
	tc := opts.TextChars
	if tc <= 0 {
		tc = defaultTextChars
	}
	out := make([]Node, 0, len(hits))
	for _, n := range hits {
		out = append(out, summary(n, tc))
		if opts.MaxMatches > 0 && len(out) >= opts.MaxMatches {
			break
		}
	}
	return out, nil
}

// SkelOptions tunes Skeleton.
type SkelOptions struct {
	// Selector roots the skeleton at the matches of this CSS selector. Empty roots
	// it at the document's top-level element(s).
	Selector string
	// MaxDepth limits how many descendant levels to include (<0 = unlimited,
	// 0 = the roots only).
	MaxDepth int
	// MaxChildren caps element children per node (0 = unlimited); excess is
	// reported via SkelNode.More.
	MaxChildren int
	// TextChars bounds each node's own-text snippet (<=0 uses the default).
	TextChars int
	// Paths attaches the stable CSS Path to every node (verbose; useful for the
	// full dump, off for shallow views).
	Paths bool
}

// SkelNode is one node of a compact structural skeleton.
type SkelNode struct {
	Tag      string            `json:"tag"`
	ID       string            `json:"id,omitempty"`
	Class    string            `json:"class,omitempty"`
	Path     string            `json:"path,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Text     string            `json:"text,omitempty"`
	Children []SkelNode        `json:"children,omitempty"`
	// More is the number of element children omitted by MaxChildren.
	More int `json:"more_children,omitempty"`
}

// Skeleton returns a compact structural view of the document (or of the nodes
// matching opts.Selector), depth- and breadth-limited so it stays small.
func (d *Doc) Skeleton(opts SkelOptions) ([]SkelNode, error) {
	tc := opts.TextChars
	if tc <= 0 {
		tc = defaultTextChars
	}
	var roots []*html.Node
	if sel := strings.TrimSpace(opts.Selector); sel != "" {
		m, err := cascadia.Compile(sel)
		if err != nil {
			return nil, fmt.Errorf("invalid selector %q: %w", sel, err)
		}
		d.doc.FindMatcher(m).Each(func(_ int, s *goquery.Selection) {
			if len(s.Nodes) > 0 {
				roots = append(roots, s.Nodes[0])
			}
		})
	} else if r := d.root(); r != nil {
		for c := r.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				roots = append(roots, c)
			}
		}
	}
	out := make([]SkelNode, 0, len(roots))
	for _, n := range roots {
		out = append(out, skel(n, 0, opts, tc))
	}
	return out, nil
}

func skel(n *html.Node, depth int, opts SkelOptions, textChars int) SkelNode {
	sn := SkelNode{Tag: n.Data, Text: truncate(ownText(n), textChars)}
	for _, a := range n.Attr {
		switch a.Key {
		case "id":
			sn.ID = a.Val
		case "class":
			sn.Class = a.Val
		}
	}
	if attrs := attrMap(n); len(attrs) > 0 {
		sn.Attrs = attrs
	}
	if opts.Paths {
		sn.Path = nodePath(n)
	}
	if opts.MaxDepth >= 0 && depth >= opts.MaxDepth {
		return sn
	}
	count := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if opts.MaxChildren > 0 && count >= opts.MaxChildren {
			sn.More++
			continue
		}
		sn.Children = append(sn.Children, skel(c, depth+1, opts, textChars))
		count++
	}
	return sn
}

// --- helpers ---

func matcherFor(pattern string, opts GrepOptions) (func(string) bool, error) {
	if opts.Regex {
		expr := pattern
		if opts.IgnoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regexp %q: %w", pattern, err)
		}
		return re.MatchString, nil
	}
	needle := pattern
	if opts.IgnoreCase {
		needle = strings.ToLower(needle)
		return func(s string) bool { return strings.Contains(strings.ToLower(s), needle) }, nil
	}
	return func(s string) bool { return strings.Contains(s, needle) }, nil
}

// summary builds a Node summary (path + compact metadata) for an element.
func summary(n *html.Node, textChars int) Node {
	out := Node{Path: nodePath(n), Tag: n.Data}
	for _, a := range n.Attr {
		switch a.Key {
		case "id":
			out.ID = a.Val
		case "class":
			out.Class = a.Val
		}
	}
	if attrs := attrMap(n); len(attrs) > 0 {
		out.Attrs = attrs
	}
	out.Text = truncate(fullText(n), textChars)
	return out
}

func attrMap(n *html.Node) map[string]string {
	if len(n.Attr) == 0 {
		return nil
	}
	m := make(map[string]string, len(n.Attr))
	for _, a := range n.Attr {
		m[a.Key] = a.Val
	}
	return m
}

// nodePath builds a stable CSS selector from the document root to n using
// :nth-of-type for disambiguation, stopping early at the nearest ancestor with an
// id. The result round-trips through Query/cascadia.
func nodePath(n *html.Node) string {
	var segs []string
	for cur := n; cur != nil && cur.Type == html.ElementNode; cur = cur.Parent {
		seg := cur.Data
		if id := getAttr(cur, "id"); id != "" {
			segs = append([]string{seg + "#" + id}, segs...)
			break
		}
		idx := 1
		for sib := cur.PrevSibling; sib != nil; sib = sib.PrevSibling {
			if sib.Type == html.ElementNode && sib.Data == cur.Data {
				idx++
			}
		}
		segs = append([]string{fmt.Sprintf("%s:nth-of-type(%d)", seg, idx)}, segs...)
	}
	return strings.Join(segs, " > ")
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// fullText returns the whitespace-collapsed concatenated text of a subtree.
func fullText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
			b.WriteByte(' ')
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// ownText returns the whitespace-collapsed text of n's direct text children only.
func ownText(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// docOrder assigns each node a document-order index for stable result sorting.
func docOrder(root *html.Node) map[*html.Node]int {
	order := map[*html.Node]int{}
	i := 0
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		order[n] = i
		i++
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(root)
	return order
}

func truncate(s string, max int) string {
	if max > 0 && len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// GroupCandidate is a detected repeating sibling group — a likely list/table of
// records (e.g. the rows of items to monitor).
type GroupCandidate struct {
	// Selector is a CSS selector (tag + classes) that matches the group's members.
	Selector string `json:"selector"`
	// Count is the largest number of matching siblings under a single parent.
	Count int `json:"count"`
	// Sample is the text of one member, so the model can recognise the right list.
	Sample string `json:"sample"`
}

// RepeatingGroups finds likely lists: elements that share a tag+class signature
// and recur across the page. It counts each signature's total occurrences
// document-wide (so it catches both sibling rows and items spread across separate
// wrapper containers — e.g. one <ul> per record). Returns up to max candidates
// (count >= minCount), most frequent first, each with a reusable CSS selector and
// a sample text, so a small model can locate "the list" in one shot.
func (d *Doc) RepeatingGroups(minCount, max, sampleChars int) []GroupCandidate {
	if minCount < 2 {
		minCount = 2
	}
	if max <= 0 {
		max = 12
	}
	if sampleChars <= 0 {
		sampleChars = 120
	}
	root := d.root()
	if root == nil {
		return nil
	}
	counts := map[string]int{}       // signature -> total occurrences in the document
	first := map[string]*html.Node{} // signature -> first occurrence (for the sample)
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				if sig := signature(c); sig != "" {
					counts[sig]++
					if _, ok := first[sig]; !ok {
						first[sig] = c
					}
				}
				walk(c)
			}
		}
	}
	walk(root)

	out := make([]GroupCandidate, 0, len(counts))
	for sig, cnt := range counts {
		if cnt < minCount {
			continue
		}
		sample := truncate(fullText(first[sig]), sampleChars)
		if strings.TrimSpace(sample) == "" {
			continue // structural/empty repeats (spacers, icons) aren't content lists
		}
		out = append(out, GroupCandidate{Selector: sig, Count: cnt, Sample: sample})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Selector < out[j].Selector
	})
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// signature builds a CSS selector token (tag + up to 3 classes) for an element.
// Class-less elements return "" (too generic to be a useful list signature),
// except list-ish containers (li/tr/article) which are kept as a tag selector.
func signature(n *html.Node) string {
	tag := n.Data
	cls := strings.Fields(getAttr(n, "class"))
	if len(cls) == 0 {
		// Class-less rows/articles are genuine record containers; class-less <li>
		// is almost always nav/menu noise, so require a class for it.
		switch tag {
		case "tr", "article":
			return tag
		}
		return ""
	}
	if len(cls) > 3 {
		cls = cls[:3]
	}
	return tag + "." + strings.Join(cls, ".")
}
