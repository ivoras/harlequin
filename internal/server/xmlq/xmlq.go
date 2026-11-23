// Package xmlq reads XML documents (e-invoices, bank statements, exports) for
// a model: an outline of the element structure, and a small XPath subset to
// pull values out, so the model never has to read raw XML.
package xmlq

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/htmlindex"
)

const (
	MaxBytes = 64 << 20
	maxNodes = 2_000_000
	maxDepth = 256
)

// Node is an element. Name is as written ("cbc:ID"); Local drops the prefix.
type Node struct {
	Name, Local string
	Attrs       []xml.Attr // Name.Space holds the prefix as written
	Text        string     // the element's own text, trimmed
	Children    []*Node
	Parent      *Node
	index       int // 1-based position among same-named siblings
}

// Parse reads an XML document.
func Parse(data []byte) (*Node, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("the XML file is %d MB, more than %d MB", len(data)>>20, MaxBytes>>20)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(label)
		if err != nil {
			return nil, fmt.Errorf("unknown encoding %q", label)
		}
		return enc.NewDecoder().Reader(in), nil
	}
	root := &Node{Name: "#document"}
	cur := root
	nodes, depth := 0, 0
	var text strings.Builder
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("not well-formed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if nodes++; nodes > maxNodes {
				return nil, errors.New("the XML file has too many elements")
			}
			if depth++; depth > maxDepth {
				return nil, errors.New("the XML file is nested too deeply")
			}
			cur.Text += text.String() // text before a child element is the parent's
			text.Reset()
			n := &Node{Name: qname(t.Name), Local: t.Name.Local, Attrs: t.Attr, Parent: cur}
			for _, s := range cur.Children {
				if s.Name == n.Name {
					n.index++
				}
			}
			n.index++
			cur.Children = append(cur.Children, n)
			cur = n
		case xml.EndElement:
			if qname(t.Name) != cur.Name {
				return nil, fmt.Errorf("not well-formed XML: </%s> closes <%s>", qname(t.Name), cur.Name)
			}
			cur.Text = strings.TrimSpace(cur.Text + text.String())
			text.Reset()
			if cur.Parent != nil {
				cur = cur.Parent
			}
			depth--
		case xml.CharData:
			text.Write(t)
		}
	}
	if cur != root {
		return nil, fmt.Errorf("not well-formed XML: <%s> is never closed", cur.Name)
	}
	if len(root.Children) == 0 {
		return nil, errors.New("no XML elements found")
	}
	return root, nil
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// Path is the node's absolute path with positions where a name repeats:
// /Invoice/cac:InvoiceLine[2]/cbc:ID.
func (n *Node) Path() string {
	var parts []string
	for x := n; x != nil && x.Parent != nil; x = x.Parent {
		p := x.Name
		if x.Parent.count(x.Name) > 1 {
			p += "[" + strconv.Itoa(x.index) + "]"
		}
		parts = append(parts, p)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return "/" + strings.Join(parts, "/")
}

func (n *Node) count(name string) int {
	c := 0
	for _, s := range n.Children {
		if s.Name == name {
			c++
		}
	}
	return c
}

// Attr returns an attribute's value by name (with or without its prefix).
func (n *Node) Attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if nameMatch(name, qname(a.Name), a.Name.Local) {
			return a.Value, true
		}
	}
	return "", false
}

// nameMatch: "cbc:ID" matches only that prefix, "ID" any prefix, "*" anything.
func nameMatch(want, name, local string) bool {
	if want == "*" {
		return true
	}
	if strings.Contains(want, ":") {
		return want == name
	}
	return want == local
}

// Match is one query result.
type Match struct {
	Path  string
	Value string            // element text, or the attribute's value
	Attrs map[string]string // the element's attributes
	Node  *Node             `json:"-"`
}

// Query evaluates a small XPath subset:
//
//	/a/b      a path from the document root
//	//b       b anywhere; a path not starting with / means the same as //…
//	a/b       steps; * matches any element; .. goes to the parent
//	b[2]      the second b among its siblings
//	b[@x='v'] b whose attribute x is v;  b[@x] b that has attribute x
//	b[c='v']  b whose child c has text v;  b[c] b that has a child c
//	.../@x    the attribute x instead of the element
//
// Names match with or without their prefix: ID matches cbc:ID.
func Query(root *Node, path string) ([]Match, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("path is empty")
	}
	attr := ""
	if i := strings.LastIndex(path, "/@"); i >= 0 && !strings.ContainsAny(path[i+2:], "/[]") {
		path, attr = path[:i], path[i+2:]
	}
	if path == "" {
		path = "//*"
	}
	if !strings.HasPrefix(path, "/") {
		path = "//" + path
	}
	cur := []*Node{root}
	for _, st := range splitSteps(path) {
		var next []*Node
		seen := map[*Node]bool{}
		add := func(n *Node) {
			if !seen[n] {
				seen[n] = true
				next = append(next, n)
			}
		}
		name, preds, err := parseStep(st.text)
		if err != nil {
			return nil, err
		}
		for _, c := range cur {
			if name == ".." {
				if c.Parent != nil {
					add(c.Parent)
				}
				continue
			}
			var cands []*Node
			if st.desc {
				walk(c, func(n *Node) {
					if n != c && nameMatch(name, n.Name, n.Local) {
						cands = append(cands, n)
					}
				})
			} else {
				for _, n := range c.Children {
					if nameMatch(name, n.Name, n.Local) {
						cands = append(cands, n)
					}
				}
			}
			for _, n := range filter(cands, preds) {
				add(n)
			}
		}
		cur = next
	}
	var out []Match
	for _, n := range cur {
		m := Match{Path: n.Path(), Value: n.Text, Node: n}
		if len(n.Attrs) > 0 {
			m.Attrs = map[string]string{}
			for _, a := range n.Attrs {
				m.Attrs[qname(a.Name)] = a.Value
			}
		}
		if attr != "" {
			v, ok := n.Attr(attr)
			if !ok {
				continue
			}
			m.Path += "/@" + attr
			m.Value = v
		}
		out = append(out, m)
	}
	return out, nil
}

type step struct {
	text string
	desc bool
}

func splitSteps(path string) []step {
	var out []step
	depth := 0
	start, desc := 0, false
	i := 0
	for i < len(path) {
		switch path[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '/':
			if depth == 0 {
				if i > start {
					out = append(out, step{path[start:i], desc})
				}
				desc = strings.HasPrefix(path[i:], "//")
				if desc {
					i++
				}
				start = i + 1
			}
		}
		i++
	}
	if start < len(path) {
		out = append(out, step{path[start:], desc})
	}
	return out
}

type pred struct {
	index       int    // [n]
	attr, child string // [@a] / [c]
	value       string
	hasValue    bool
}

func parseStep(s string) (string, []pred, error) {
	name := s
	var preds []pred
	if i := strings.IndexByte(s, '['); i >= 0 {
		name = s[:i]
		rest := s[i:]
		for rest != "" {
			if rest[0] != '[' {
				return "", nil, fmt.Errorf("bad step %q", s)
			}
			j := strings.IndexByte(rest, ']')
			if j < 0 {
				return "", nil, fmt.Errorf("unclosed [ in %q", s)
			}
			p, err := parsePred(strings.TrimSpace(rest[1:j]))
			if err != nil {
				return "", nil, fmt.Errorf("%v in %q", err, s)
			}
			preds = append(preds, p)
			rest = rest[j+1:]
		}
	}
	if name == "" {
		return "", nil, fmt.Errorf("empty step in path")
	}
	return name, preds, nil
}

func parsePred(s string) (pred, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 {
			return pred{}, errors.New("positions start at 1")
		}
		return pred{index: n}, nil
	}
	var p pred
	key := s
	if k, v, ok := strings.Cut(s, "="); ok {
		key, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) < 2 || (v[0] != '\'' && v[0] != '"') || v[len(v)-1] != v[0] {
			return p, errors.New("compare with a quoted value, e.g. [@schemeID='9934']")
		}
		p.value, p.hasValue = v[1:len(v)-1], true
	}
	if a, ok := strings.CutPrefix(key, "@"); ok {
		p.attr = a
	} else {
		p.child = key
	}
	return p, nil
}

func filter(ns []*Node, preds []pred) []*Node {
	for _, p := range preds {
		var keep []*Node
		for i, n := range ns {
			switch {
			case p.index > 0:
				// Position among this step's matches under the same parent.
				pos := 0
				for _, m := range ns[:i+1] {
					if m.Parent == n.Parent {
						pos++
					}
				}
				if pos == p.index {
					keep = append(keep, n)
				}
			case p.attr != "":
				if v, ok := n.Attr(p.attr); ok && (!p.hasValue || v == p.value) {
					keep = append(keep, n)
				}
			default:
				for _, c := range n.Children {
					if nameMatch(p.child, c.Name, c.Local) && (!p.hasValue || c.Text == p.value) {
						keep = append(keep, n)
						break
					}
				}
			}
		}
		ns = keep
	}
	return ns
}

func walk(n *Node, f func(*Node)) {
	f(n)
	for _, c := range n.Children {
		walk(c, f)
	}
}

// Outline lists each distinct element path once (positions dropped) with how
// often it occurs, its attributes and its first value: the structure of the
// file at a glance, in document order.
func Outline(root *Node, maxLines int) string {
	type entry struct {
		path  string
		count int
		value string
		attrs map[string]bool
	}
	var order []string
	byPath := map[string]*entry{}
	var visit func(n *Node, prefix string)
	visit = func(n *Node, prefix string) {
		for _, c := range n.Children {
			p := prefix + "/" + c.Name
			e := byPath[p]
			if e == nil {
				e = &entry{path: p, attrs: map[string]bool{}}
				byPath[p] = e
				order = append(order, p)
			}
			e.count++
			if e.value == "" && c.Text != "" {
				e.value = c.Text
			}
			for _, a := range c.Attrs {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					e.attrs[qname(a.Name)+"="+a.Value] = true
				}
			}
			visit(c, p)
		}
	}
	visit(root, "")
	var sb strings.Builder
	for i, p := range order {
		if i >= maxLines {
			fmt.Fprintf(&sb, "… %d more element paths not shown; query a part with path, e.g. //cac:InvoiceLine\n", len(order)-maxLines)
			break
		}
		e := byPath[p]
		depth := strings.Count(p, "/") - 1
		line := strings.Repeat("  ", depth) + p[strings.LastIndexByte(p, '/')+1:]
		if e.count > 1 {
			line += fmt.Sprintf(" (×%d)", e.count)
		}
		if len(e.attrs) > 0 {
			var as []string
			for a := range e.attrs {
				as = append(as, a)
			}
			if len(as) > 4 {
				as = append(as[:4], "…")
			}
			line += " [" + strings.Join(as, " ") + "]"
		}
		if e.value != "" {
			line += " = " + strconv.Quote(Short(e.value, 80))
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// Short cuts long values (embedded base64 attachments) for display.
func Short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + fmt.Sprintf("… (%d characters)", len(r))
}
