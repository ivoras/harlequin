package docxedit

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// The small Markdown subset accepted in inserted text.

type block struct {
	kind  byte   // 'p' paragraph, 'h' heading, 'l' list item
	level int    // heading level
	text  string // inline Markdown (list marker and heading marks removed)
	raw   string // the line as typed (a list item keeps its marker here)
}

var (
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	listRE    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+(.*)$`)
	spanRE    = regexp.MustCompile(`\*\*(.+?)\*\*|\*(.+?)\*`)
	sepCellRE = regexp.MustCompile(`^:?-{1,}:?$`)
)

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func isSepRow(cells []string) bool {
	for _, c := range cells {
		if !sepCellRE.MatchString(c) {
			return false
		}
	}
	return len(cells) > 0
}

// parseBlocks reads inserted text: paragraphs separated by empty lines, #
// headings, list items; or exactly one table row (row non-nil).
func parseBlocks(s string) (blocks []block, row []string, err error) {
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			t := strings.Join(buf, " ")
			blocks = append(blocks, block{kind: 'p', text: t, raw: t})
			buf = nil
		}
	}
	var rows [][]string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "|"):
			flush()
			rows = append(rows, splitRow(t))
		case headingRE.MatchString(t):
			flush()
			m := headingRE.FindStringSubmatch(t)
			blocks = append(blocks, block{kind: 'h', level: len(m[1]), text: strings.TrimSpace(m[2]), raw: t})
		case listRE.MatchString(line):
			flush()
			blocks = append(blocks, block{kind: 'l', text: listRE.FindStringSubmatch(line)[1], raw: t})
		default:
			buf = append(buf, t)
		}
	}
	flush()
	if len(rows) > 0 {
		var data [][]string
		for _, r := range rows {
			if !isSepRow(r) {
				data = append(data, r)
			}
		}
		if len(data) != 1 || len(rows) != 1 || len(blocks) > 0 {
			return nil, nil, fmt.Errorf("creating new tables in an existing Word file is not supported; only a single table row (| a | b |) can be inserted next to an existing row of a table, and it cannot be mixed with other text")
		}
		return nil, data[0], nil
	}
	return blocks, nil, nil
}

type span struct {
	text         string
	bold, italic bool
}

// spans splits inline Markdown into runs of text: **bold**, *italic*; anything
// else stays literal.
func spans(s string) []span {
	var out []span
	last := 0
	for _, m := range spanRE.FindAllStringSubmatchIndex(s, -1) {
		if m[0] > last {
			out = append(out, span{text: s[last:m[0]]})
		}
		if m[2] >= 0 {
			out = append(out, span{text: s[m[2]:m[3]], bold: true})
		} else {
			out = append(out, span{text: s[m[4]:m[5]], italic: true})
		}
		last = m[1]
	}
	if last < len(s) {
		out = append(out, span{text: s[last:]})
	}
	return out
}

func plain(ss []span) string {
	var sb strings.Builder
	for _, s := range ss {
		sb.WriteString(s.text)
	}
	return sb.String()
}

// esc escapes text for an XML text node and drops characters XML cannot carry.
func esc(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			sb.WriteString("&amp;")
		case r == '<':
			sb.WriteString("&lt;")
		case r == '>':
			sb.WriteString("&gt;")
		case r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r != 0xFFFE && r != 0xFFFF && !(r >= 0xD800 && r <= 0xDFFF)):
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// normalize collapses whitespace runs to one space, trims, and drops the
// Markdown emphasis marks * and _, so a passage quoted from read_doc's
// rendering matches the paragraph text. orig maps each result rune to its
// index in src.
func normalize(src []rune) (out []rune, orig []int) {
	for i, r := range src {
		switch {
		case r == '*' || r == '_':
		case unicode.IsSpace(r):
			if len(out) > 0 && out[len(out)-1] != ' ' {
				out = append(out, ' ')
				orig = append(orig, i)
			}
		default:
			out = append(out, r)
			orig = append(orig, i)
		}
	}
	if n := len(out); n > 0 && out[n-1] == ' ' {
		out, orig = out[:n-1], orig[:n-1]
	}
	return
}

// stripMarkdown removes what read_doc's rendering adds around a quoted line:
// heading marks, list markers and quote marks.
func stripMarkdown(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSpace(strings.TrimLeft(line, ">"))
	if m := headingRE.FindStringSubmatch(line); m != nil {
		return m[2]
	}
	if m := listRE.FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return line
}

// Normalize and StripMarkdown expose the passage matching rules (whitespace
// collapsed, * and _ ignored, heading/list/quote marks removed) to other
// editors of uploaded files, so every "quote it as read_doc shows it" matches alike.
func Normalize(s string) string {
	n, _ := normalize([]rune(s))
	return string(n)
}

func StripMarkdown(line string) string { return stripMarkdown(line) }
