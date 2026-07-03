package docling

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// This file rebuilds plain text (with page offsets) from a DoclingDocument —
// docling's lossless JSON format. The document body is a tree of $ref nodes
// pointing into the texts/groups/tables arrays; each text and table carries
// provenance with the 1-based page number it came from. We walk the body in
// reading order, render each item as a Markdown-ish block, and record the rune
// offset at which every page begins (same convention as pdfextract ingestion:
// pageStarts[i] is where page i+1 starts).

type doclingDoc struct {
	Body   doclingNode    `json:"body"`
	Texts  []doclingText  `json:"texts"`
	Groups []doclingNode  `json:"groups"`
	Tables []doclingTable `json:"tables"`
}

type doclingRef struct {
	Ref string `json:"$ref"`
}

type doclingNode struct {
	Children []doclingRef `json:"children"`
}

type doclingProv struct {
	PageNo int `json:"page_no"`
}

type doclingText struct {
	Label string        `json:"label"`
	Prov  []doclingProv `json:"prov"`
	Text  string        `json:"text"`
	doclingNode
}

type doclingTable struct {
	Prov []doclingProv `json:"prov"`
	Data struct {
		Grid [][]struct {
			Text string `json:"text"`
		} `json:"grid"`
	} `json:"data"`
	doclingNode
}

// flattener accumulates blocks and page starts while walking the body tree.
type flattener struct {
	doc      *doclingDoc
	sb       strings.Builder
	runeOff  int
	lastPage int
	starts   []int
	visited  map[string]bool // cycle guard on $refs
}

func flattenDoclingJSON(raw json.RawMessage) (string, []int, error) {
	var doc doclingDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", nil, fmt.Errorf("docling: parse json_content: %w", err)
	}
	f := &flattener{doc: &doc, visited: map[string]bool{}}
	f.walk(doc.Body.Children)
	return f.sb.String(), f.starts, nil
}

func (f *flattener) walk(children []doclingRef) {
	for _, ch := range children {
		if f.visited[ch.Ref] {
			continue
		}
		f.visited[ch.Ref] = true
		kind, idx, ok := parseRef(ch.Ref)
		if !ok {
			continue
		}
		switch kind {
		case "texts":
			if idx < len(f.doc.Texts) {
				t := f.doc.Texts[idx]
				f.emit(renderText(t), provPage(t.Prov))
				f.walk(t.Children)
			}
		case "groups":
			if idx < len(f.doc.Groups) {
				f.walk(f.doc.Groups[idx].Children)
			}
		case "tables":
			if idx < len(f.doc.Tables) {
				t := f.doc.Tables[idx]
				f.emit(renderTable(t), provPage(t.Prov))
				f.walk(t.Children)
			}
		}
	}
}

// emit appends one block, advancing page starts up to its page. Blocks are
// separated by blank lines; a page that starts mid-block is recorded at the
// block's beginning (page mapping is per chunk, so block granularity is fine).
func (f *flattener) emit(block string, page int) {
	block = strings.TrimSpace(block)
	if block == "" {
		return
	}
	if f.sb.Len() > 0 {
		f.sb.WriteString("\n\n")
		f.runeOff += 2
	}
	if page <= 0 {
		page = max(f.lastPage, 1)
	}
	for p := f.lastPage; p < page; p++ {
		f.starts = append(f.starts, f.runeOff)
	}
	f.lastPage = max(f.lastPage, page)
	f.sb.WriteString(block)
	f.runeOff += utf8.RuneCountInString(block)
}

// renderText renders one text item as a Markdown-ish block.
func renderText(t doclingText) string {
	switch t.Label {
	case "section_header", "title":
		return "## " + t.Text
	case "list_item":
		return "- " + t.Text
	case "page_header", "page_footer":
		return "" // furniture that leaked into the body: drop
	default:
		return t.Text
	}
}

// renderTable renders a table's cell grid as pipe-separated rows.
func renderTable(t doclingTable) string {
	var rows []string
	for _, row := range t.Data.Grid {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = strings.TrimSpace(c.Text)
		}
		if r := strings.Trim(strings.Join(cells, " | "), " |"); r != "" {
			rows = append(rows, strings.Join(cells, " | "))
		}
	}
	return strings.Join(rows, "\n")
}

// parseRef splits "#/texts/12" into ("texts", 12).
func parseRef(ref string) (kind string, idx int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	if len(parts) != 2 {
		return "", 0, false
	}
	n := 0
	for _, r := range parts[1] {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		n = n*10 + int(r-'0')
	}
	return parts[0], n, true
}

func provPage(prov []doclingProv) int {
	if len(prov) == 0 {
		return 0
	}
	return prov[0].PageNo
}
