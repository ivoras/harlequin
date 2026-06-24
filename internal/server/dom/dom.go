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
	Path  string            `json:"path,omitempty"`
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
	hits, err := grepRawNodes(ctx, pattern, opts)
	if err != nil {
		return nil, err
	}
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

// grepRawNodes returns the matching element nodes (document order), the shared
// core of GrepNode and the context-bearing GrepContext.
func grepRawNodes(ctx *html.Node, pattern string, opts GrepOptions) ([]*html.Node, error) {
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
	return hits, nil
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
		// id and class have dedicated fields on Node/SkelNode; don't duplicate them.
		if a.Key == "id" || a.Key == "class" {
			continue
		}
		m[a.Key] = a.Val
	}
	if len(m) == 0 {
		return nil
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

// --- record extraction ---

// Record is one item of a repeating list: a stable Path, a short text summary,
// and outbound links. One Records call yields the list as comparable rows so a
// model can read, compare, filter, or count items in a single call rather than
// grepping each field. It extracts no semantics (no number/price parsing) — the
// model reads the item text and decides what matters.
type Record struct {
	Path  string   `json:"path,omitempty"`
	Text  string   `json:"text,omitempty"`
	Links []string `json:"links,omitempty"`
}

// linksIn returns up to max distinct href/src URLs found in n's subtree.
func linksIn(n *html.Node, max int) []string {
	if max <= 0 {
		max = 4
	}
	seen := map[string]bool{}
	var out []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				for _, a := range c.Attr {
					if (a.Key == "href" || a.Key == "src") && a.Val != "" && !seen[a.Val] {
						seen[a.Val] = true
						out = append(out, a.Val)
					}
				}
			}
			walk(c)
			if len(out) >= max {
				return
			}
		}
	}
	walk(n)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// Records returns the elements matching selector as comparable records (short
// text + outbound links). max caps the rows (<=0 = 30 default); textChars bounds
// each text snippet (<=0 uses the default).
func (d *Doc) Records(selector string, max, textChars int) ([]Record, error) {
	m, err := cascadia.Compile(strings.TrimSpace(selector))
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	if max <= 0 {
		max = 30
	}
	if textChars <= 0 {
		textChars = defaultTextChars
	}
	var out []Record
	d.doc.FindMatcher(m).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if len(s.Nodes) == 0 {
			return true
		}
		n := s.Nodes[0]
		out = append(out, Record{
			Path:  nodePath(n),
			Text:  truncate(fullText(n), textChars),
			Links: linksIn(n, 4),
		})
		return len(out) < max
	})
	return out, nil
}

// GroupCandidate is a detected repeating sibling group — a likely list/table of
// records (e.g. the rows of items to monitor or compare).
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

// --- match context (neighbourhood around a located node) ---

// ContextOptions controls how much surrounding DOM Grep/Query attach to each
// match, so a value (e.g. a price) can be read together with nearby labels,
// names, and links without further calls. It makes no assumption about page
// structure: it walks sibling and ancestor elements generically.
type ContextOptions struct {
	// Siblings is how many preceding and following sibling elements to include
	// on each side of the match (0 = none).
	Siblings int
	// Ancestors is how many ancestor levels to include, nearest first (0 = none).
	// Ancestor text is the element's whole-subtree text (bounded), so a label or
	// name held in a cousin subtree is surfaced alongside the match.
	Ancestors int
	// TextChars bounds each context node's text snippet (<=0 uses the default).
	TextChars int
}

// MatchContext is a located node plus its DOM neighbourhood.
type MatchContext struct {
	Match     Node   `json:"match"`
	Prev      []Node `json:"prev,omitempty"`      // preceding siblings, nearest last
	Next      []Node `json:"next,omitempty"`      // following siblings, nearest first
	Ancestors []Node `json:"ancestors,omitempty"` // enclosing elements, nearest first
}

func contextFor(n *html.Node, opts ContextOptions) MatchContext {
	tc := opts.TextChars
	if tc <= 0 {
		tc = defaultTextChars
	}
	mc := MatchContext{Match: summary(n, tc)}
	// Context nodes are read for their content (a nearby name/label/link), never
	// queried, so they carry no Path — the deep nth-of-type chains would dominate
	// the byte budget and crowd out other matches. Only Match keeps its Path.
	ctxSummary := func(e *html.Node) Node {
		s := summary(e, tc)
		s.Path = ""
		return s
	}
	// Preceding siblings (collect nearest-first, then reverse so output reads
	// in document order).
	cnt := 0
	for s := n.PrevSibling; s != nil && cnt < opts.Siblings; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			mc.Prev = append(mc.Prev, ctxSummary(s))
			cnt++
		}
	}
	for i, j := 0, len(mc.Prev)-1; i < j; i, j = i+1, j-1 {
		mc.Prev[i], mc.Prev[j] = mc.Prev[j], mc.Prev[i]
	}
	cnt = 0
	for s := n.NextSibling; s != nil && cnt < opts.Siblings; s = s.NextSibling {
		if s.Type == html.ElementNode {
			mc.Next = append(mc.Next, ctxSummary(s))
			cnt++
		}
	}
	cnt = 0
	for p := n.Parent; p != nil && p.Type == html.ElementNode && cnt < opts.Ancestors; p = p.Parent {
		mc.Ancestors = append(mc.Ancestors, ctxSummary(p))
		cnt++
	}
	return mc
}

// GrepContext is Grep with each match wrapped in its DOM neighbourhood.
func (d *Doc) GrepContext(pattern string, opts GrepOptions, cx ContextOptions) ([]MatchContext, error) {
	hits, err := grepRawNodes(d.root(), pattern, opts)
	if err != nil {
		return nil, err
	}
	out := make([]MatchContext, 0, len(hits))
	for _, n := range hits {
		out = append(out, contextFor(n, cx))
		if opts.MaxMatches > 0 && len(out) >= opts.MaxMatches {
			break
		}
	}
	return out, nil
}

// QueryContext is Query with each match wrapped in its DOM neighbourhood. Best
// for a selector that matches one or a few nodes; for long lists prefer Records.
func (d *Doc) QueryContext(selector string, max int, cx ContextOptions) ([]MatchContext, error) {
	m, err := cascadia.Compile(strings.TrimSpace(selector))
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	var out []MatchContext
	d.doc.FindMatcher(m).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if len(s.Nodes) > 0 {
			out = append(out, contextFor(s.Nodes[0], cx))
		}
		return max <= 0 || len(out) < max
	})
	return out, nil
}

// --- flattened text view + family selection (WebFetchDOM grep/selector modes) ---

// flattenTags are the only element tags kept in the flattened view and family
// descriptors; everything else is skipped (but still descended into).
var flattenTags = map[string]bool{
	"div": true, "span": true, "a": true, "li": true, "ol": true, "ul": true, "p": true,
	"table": true, "tr": true, "td": true, "th": true, "img": true,
}

// descAttrs are the only attributes shown in a descriptor, in this order.
var descAttrs = []string{"class", "href", "alt", "src"}

// descriptor renders an element as tag(attr="v" …) using only descAttrs.
func descriptor(n *html.Node) string {
	var parts []string
	for _, k := range descAttrs {
		if v := getAttr(n, k); v != "" {
			parts = append(parts, fmt.Sprintf("%s=%q", k, v))
		}
	}
	if len(parts) == 0 {
		return n.Data
	}
	return n.Data + "(" + strings.Join(parts, " ") + ")"
}

// Flatten renders the document as one line per element of an allowed tag: the
// dotted path of allowed-tag ancestors (each a descriptor) ending at the element,
// then ": <own text>". Non-allowed tags are skipped from the path but descended
// into, so their allowed descendants still appear.
func (d *Doc) Flatten() []string {
	root := d.root()
	if root == nil {
		return nil
	}
	var out []string
	var stack []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if flattenTags[c.Data] {
				stack = append(stack, descriptor(c))
				out = append(out, strings.Join(stack, ".")+": "+truncate(ownText(c), 160))
				walk(c)
				stack = stack[:len(stack)-1]
			} else {
				walk(c)
			}
		}
	}
	walk(root)
	return out
}

// GrepFlatten greps the flattened view plus any extra lines (case-insensitive
// substring) and returns the matching lines with contextLines of surrounding
// lines on each side; match lines are prefixed "> ", context lines "  ", and
// non-adjacent groups are separated by "--". Empty if nothing matches. extra is
// appended to the flattened lines so callers can include e.g. the link list in
// the grep corpus.
func (d *Doc) GrepFlatten(pattern string, contextLines int, extra []string) string {
	if contextLines < 0 {
		contextLines = 0
	}
	lines := append(d.Flatten(), extra...)
	needle := strings.ToLower(pattern)
	keep := make([]bool, len(lines))
	match := make([]bool, len(lines))
	found := false
	for i, ln := range lines {
		if strings.Contains(strings.ToLower(ln), needle) {
			match[i] = true
			found = true
			for j := i - contextLines; j <= i+contextLines; j++ {
				if j >= 0 && j < len(lines) {
					keep[j] = true
				}
			}
		}
	}
	if !found {
		return ""
	}
	var out []string
	prev := -2
	for i := range lines {
		if !keep[i] {
			continue
		}
		if prev >= 0 && i > prev+1 {
			out = append(out, "--")
		}
		mark := "  "
		if match[i] {
			mark = "> "
		}
		out = append(out, mark+lines[i])
		prev = i
	}
	return strings.Join(out, "\n")
}

// DescNode is an element rendered as a descriptor plus its (bounded) text.
type DescNode struct {
	Desc string
	Text string
}

// Family is a selector match with its immediate relatives.
type Family struct {
	Match    DescNode
	Parent   *DescNode
	Siblings []DescNode // up to the requested limit, nearest first
	Children []DescNode // up to the requested limit, in document order
}

func descOf(n *html.Node, textMax int) DescNode {
	return DescNode{Desc: descriptor(n), Text: truncate(fullText(n), textMax)}
}

// SelectFamily matches selector (standard CSS; comma = union, e.g.
// "div.item.muted-item, div.product-card") and returns each match with its
// element parent, up to sibLimit nearest sibling elements, and up to childLimit
// child elements.
func (d *Doc) SelectFamily(selector string, maxMatches, sibLimit, childLimit int) ([]Family, error) {
	m, err := cascadia.Compile(strings.TrimSpace(selector))
	if err != nil {
		return nil, fmt.Errorf("invalid selector %q: %w", selector, err)
	}
	if sibLimit <= 0 {
		sibLimit = 3
	}
	if childLimit <= 0 {
		childLimit = 3
	}
	var fams []Family
	d.doc.FindMatcher(m).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if len(s.Nodes) == 0 {
			return true
		}
		n := s.Nodes[0]
		f := Family{Match: descOf(n, 200)}
		for p := n.Parent; p != nil; p = p.Parent {
			if p.Type == html.ElementNode {
				dn := descOf(p, 160)
				f.Parent = &dn
				break
			}
		}
		f.Siblings = nearestSiblings(n, sibLimit)
		for c := n.FirstChild; c != nil && len(f.Children) < childLimit; c = c.NextSibling {
			if c.Type == html.ElementNode {
				f.Children = append(f.Children, descOf(c, 120))
			}
		}
		fams = append(fams, f)
		return maxMatches <= 0 || len(fams) < maxMatches
	})
	return fams, nil
}

// nearestSiblings returns up to limit element siblings of n, following siblings
// first then preceding, nearest first.
func nearestSiblings(n *html.Node, limit int) []DescNode {
	var next, prev []*html.Node
	for s := n.NextSibling; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode {
			next = append(next, s)
		}
	}
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode {
			prev = append(prev, s)
		}
	}
	var out []DescNode
	for i := 0; len(out) < limit && (i < len(next) || i < len(prev)); i++ {
		if i < len(next) && len(out) < limit {
			out = append(out, descOf(next[i], 120))
		}
		if i < len(prev) && len(out) < limit {
			out = append(out, descOf(prev[i], 120))
		}
	}
	return out
}

// FamiliesYAML renders families as YAML.
func FamiliesYAML(fams []Family) string {
	var b strings.Builder
	for _, f := range fams {
		fmt.Fprintf(&b, "- desc: %s\n", yamlScalar(f.Match.Desc))
		fmt.Fprintf(&b, "  text: %s\n", yamlScalar(f.Match.Text))
		if f.Parent != nil {
			fmt.Fprintf(&b, "  parent: %s\n", yamlScalar(f.Parent.Desc))
		}
		if len(f.Siblings) > 0 {
			b.WriteString("  siblings:\n")
			for _, s := range f.Siblings {
				fmt.Fprintf(&b, "    - desc: %s\n      text: %s\n", yamlScalar(s.Desc), yamlScalar(s.Text))
			}
		}
		if len(f.Children) > 0 {
			b.WriteString("  children:\n")
			for _, c := range f.Children {
				fmt.Fprintf(&b, "    - desc: %s\n      text: %s\n", yamlScalar(c.Desc), yamlScalar(c.Text))
			}
		}
	}
	return b.String()
}

// yamlScalar renders s as a safe double-quoted YAML scalar.
func yamlScalar(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// LinkLines returns <a> elements formatted as `<href>[ data-page/rel]: link
// text`, sorted alphabetically. A link is unique by href plus its data-page/rel
// (so pagination links — same href, differing data-page or rel=next/prev —
// survive instead of collapsing). Only absolute or root-relative hrefs (starting
// with "/", "http://" or "https://") are kept; fragment, javascript:, and bare
// relative links, and anchors with no visible text, are dropped.
func (d *Doc) LinkLines() []string {
	root := d.root()
	if root == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if c.Data == "a" {
				href := getAttr(c, "href")
				text := strings.TrimSpace(fullText(c))
				if text != "" && isNavigableHref(href) {
					// Distinguishing attrs let pagination survive: page links often
					// share one href and differ only by data-page (JS pagination) or
					// rel=next/prev. Key the dedup on them and surface them.
					dp, rel := getAttr(c, "data-page"), getAttr(c, "rel")
					key := href + "\x00" + dp + "\x00" + rel
					if !seen[key] {
						seen[key] = true
						suffix := ""
						if dp != "" {
							suffix += " [data-page=" + dp + "]"
						}
						if rel != "" {
							suffix += " [rel=" + rel + "]"
						}
						out = append(out, href+suffix+": "+truncate(text, 160))
					}
				}
			}
			walk(c)
		}
	}
	walk(root)
	sort.Strings(out)
	return out
}

// isNavigableHref reports whether href is an absolute or root-relative URL.
func isNavigableHref(href string) bool {
	return strings.HasPrefix(href, "/") ||
		strings.HasPrefix(href, "https://") ||
		strings.HasPrefix(href, "http://")
}
