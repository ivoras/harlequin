package sheetdb

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ivoras/harlequin/internal/server/mdxlsx"
)

var mdHeadingRE = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)

type mdTable struct {
	name string
	rows [][]string // rows[0] is the header
}

// parseMDTables finds the pipe tables in a document's Markdown text (how the
// converter renders tables of a PDF or Word file). A table whose header
// repeats the previous table's is its continuation (a table split across
// pages), so it is appended. Each table is named after the heading above it.
func parseMDTables(text string) []mdTable {
	var out []mdTable
	heading := ""
	var cur [][]string
	flush := func() {
		if len(cur) < 2 { // a header alone is not data
			cur = nil
			return
		}
		if n := len(out); n > 0 && sameRow(out[n-1].rows[0], cur[0]) {
			out[n-1].rows = append(out[n-1].rows, cur[1:]...)
		} else {
			name := heading
			if name == "" {
				name = fmt.Sprintf("Table %d", len(out)+1)
			}
			out = append(out, mdTable{name: name, rows: cur})
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") {
			cells := mdxlsx.SplitRow(t)
			if mdxlsx.IsSeparator(cells) {
				continue
			}
			for i, c := range cells {
				cells[i] = strings.TrimSpace(strings.Trim(strings.TrimSpace(c), "*_`"))
			}
			cur = append(cur, cells)
			continue
		}
		if t == "" && cur != nil {
			continue // a blank line inside a table block is not its end yet
		}
		flush()
		if m := mdHeadingRE.FindStringSubmatch(t); m != nil {
			heading = strings.Trim(m[1], "*_ ")
		}
	}
	flush()
	return out
}

func sameRow(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// readMDTables streams the document's tables like sheets: the first row of
// each is its header, values are typed like CSV values.
func readMDTables(text string, sheet func(name string, hidden bool) error, row func(int, []any, []string) error) error {
	tables := parseMDTables(text)
	if len(tables) == 0 {
		return errNoTables
	}
	for _, t := range tables {
		if err := sheet(t.name, false); err != nil {
			return err
		}
		for i, r := range t.rows {
			cells := make([]any, len(r))
			has := false
			for j, s := range r {
				if s == "" {
					continue
				}
				has = true
				if i == 0 {
					cells[j] = s // the header stays text
				} else {
					cells[j] = csvValue(s)
				}
			}
			if has {
				if err := row(i+1, cells, nil); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
