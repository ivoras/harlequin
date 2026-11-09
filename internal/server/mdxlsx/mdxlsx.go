// Package mdxlsx turns the Markdown a spreadsheet converts to (docling's
// output for .xlsx: one pipe table per data region) back into an Excel
// workbook, with optional headings naming sheets. The format it reads:
//
//   - A heading line (# … ######) starts a new sheet named by its text; an
//     optional "Sheet:" prefix is dropped. Tables before any heading go on the
//     first sheet.
//   - Each pipe table becomes a block of rows; several tables on one sheet are
//     stacked with one empty row between them (which is how docling splits a
//     sheet into tables, so docling's own output round-trips).
//   - A table's first row is its header (bold); a |---| separator row right
//     after it is skipped. "\|" is a literal pipe inside a cell, and HTML
//     entities are decoded (docling writes a pipe as "&#124;").
//   - Cells holding a plain number (e.g. 1500, -2.5, 0.95, 1e6) become numeric
//     cells written exactly as given, keeping their decimals on display ("9.50"
//     shows as 9.50); "=…" becomes a formula; everything else, including
//     numbers with leading zeros or over 15 digits, is text.
//   - Other lines (paragraphs, lists) are not part of any sheet and are counted
//     in Stats.Ignored.
//
// Written with the standard library only: a minimal SpreadsheetML package
// (inline strings, one bold style, column widths), no shared strings.
package mdxlsx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Sheet is one worksheet: its name and its tables in order (rows of cells).
type Sheet struct {
	Name   string
	Tables [][][]string
}

// Stats summarises a conversion for the caller's result message.
type Stats struct {
	Sheets, Tables, Rows int
	Ignored              int // non-table, non-heading lines
}

const (
	maxRows      = 1 << 20 // Excel's limit per sheet
	maxCols      = 16384
	maxSheetName = 31
)

var (
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
	sepCellRe = regexp.MustCompile(`^:?-+:?$`)
	// A plain decimal number; leading zeros (other than "0" / "0.x") stay text.
	numberRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// Parse reads Markdown in the format described in the package comment.
func Parse(md string) ([]Sheet, Stats) {
	var sheets []Sheet
	var st Stats
	cur := -1
	ensure := func() {
		if cur < 0 {
			sheets = append(sheets, Sheet{})
			cur = 0
		}
	}
	var table [][]string // table being read
	flush := func() {
		if table != nil {
			ensure()
			sheets[cur].Tables = append(sheets[cur].Tables, table)
			table = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "|"):
			cells := splitRow(t)
			if len(table) == 1 && isSeparator(cells) {
				continue
			}
			table = append(table, cells)
		case headingRe.MatchString(t):
			flush()
			name := headingRe.FindStringSubmatch(t)[1]
			name = strings.TrimSpace(strings.TrimPrefix(name, "Sheet:"))
			sheets = append(sheets, Sheet{Name: name})
			cur = len(sheets) - 1
		default:
			flush()
			if t != "" {
				st.Ignored++
			}
		}
	}
	flush()
	for _, s := range sheets {
		st.Tables += len(s.Tables)
		for _, tb := range s.Tables {
			st.Rows += len(tb)
		}
	}
	st.Sheets = len(sheets)
	return sheets, st
}

// splitRow splits "| a | b \| c |" into ["a", "b | c"].
func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		line = line[:len(line)-1]
	}
	var cells []string
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch {
		case line[i] == '\\' && i+1 < len(line) && line[i+1] == '|':
			b.WriteByte('|')
			i++
		case line[i] == '|':
			cells = append(cells, cell(b.String()))
			b.Reset()
		default:
			b.WriteByte(line[i])
		}
	}
	return append(cells, cell(b.String()))
}

func cell(s string) string {
	return strings.TrimSpace(html.UnescapeString(s))
}

func isSeparator(cells []string) bool {
	for _, c := range cells {
		if !sepCellRe.MatchString(c) {
			return false
		}
	}
	return len(cells) > 0
}

// Convert parses md and builds the workbook. It fails if there is no table, or
// if a table has a header row but no data rows.
func Convert(md string) ([]byte, Stats, error) {
	sheets, st := Parse(md)
	if st.Tables == 0 {
		return nil, st, errors.New("no Markdown table found (rows must start with |)")
	}
	// A table is a header row plus data. A header with nothing under it is almost
	// always a truncated or half-built call; refuse it rather than save a workbook
	// that looks done but is empty.
	for i, sh := range sheets {
		for j, tb := range sh.Tables {
			if len(tb) < 2 {
				name := sh.Name
				if name == "" {
					name = fmt.Sprintf("Sheet%d", i+1)
				}
				return nil, st, fmt.Errorf("table %d on sheet %q has a header row but no data rows (is the content cut off?); add the data rows below the header", j+1, name)
			}
		}
	}
	data, err := Write(sheets)
	return data, st, err
}

// Write builds an .xlsx from sheets. Sheet names are made valid and unique.
func Write(sheets []Sheet) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, errors.New("no sheets")
	}
	names := sheetNames(sheets)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(body))
		return err
	}

	var ct, wb, rels strings.Builder
	ct.WriteString(xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	wb.WriteString(xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range sheets {
		n := i + 1
		fmt.Fprintf(&ct, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, esc(names[i]), n, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		body, err := sheetXML(sheets[i])
		if err != nil {
			return nil, fmt.Errorf("sheet %q: %w", names[i], err)
		}
		if err := add(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), body); err != nil {
			return nil, err
		}
	}
	ct.WriteString(`</Types>`)
	// fullCalcOnLoad: formulas have no cached values, so Excel computes on open.
	wb.WriteString(`</sheets><calcPr fullCalcOnLoad="1"/></workbook>`)
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, len(sheets)+1)

	for _, f := range []struct{ name, body string }{
		{"[Content_Types].xml", ct.String()},
		{"_rels/.rels", xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", wb.String()},
		{"xl/_rels/workbook.xml.rels", rels.String()},
		{"xl/styles.xml", stylesXML},
	} {
		if err := add(f.name, f.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// maxDecimals bounds the fixed-decimals number formats ("0.0" … "0.0000000000").
const maxDecimals = 10

// stylesXML: style 0 = default, 1 = bold (header rows); then, for d = 1…10
// decimals, style 2d = "0.<d zeros>" and 2d+1 = the same in bold (styleFor).
var stylesXML = func() string {
	var fmts, xfs strings.Builder
	for d := 1; d <= maxDecimals; d++ {
		fmt.Fprintf(&fmts, `<numFmt numFmtId="%d" formatCode="0.%s"/>`, 163+d, strings.Repeat("0", d))
		fmt.Fprintf(&xfs, `<xf numFmtId="%d" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>`+
			`<xf numFmtId="%d" fontId="1" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyFont="1"/>`, 163+d, 163+d)
	}
	return xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		fmt.Sprintf(`<numFmts count="%d">%s</numFmts>`, maxDecimals, fmts.String()) +
		stylesBody(2+2*maxDecimals, xfs.String())
}()

// styleFor returns the cell style index for a header/body cell holding v.
func styleFor(v string, header bool) int {
	s := 0
	if header {
		s = 1
	}
	if i := strings.IndexByte(v, '.'); i >= 0 && isNumber(v) && !strings.ContainsAny(v, "eE") {
		if d := len(v) - i - 1; d >= 1 && d <= maxDecimals {
			s += 2 * d
		}
	}
	return s
}

func stylesBody(nXfs int, extraXfs string) string {
	return `<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
		`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
		`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
		fmt.Sprintf(`<cellXfs count="%d">`, nXfs) + `<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
		`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` + extraXfs + `</cellXfs>` +
		`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`
}

func sheetXML(s Sheet) (string, error) {
	var rows strings.Builder
	widths := map[int]int{}
	r := 0
	for ti, tb := range s.Tables {
		if ti > 0 {
			r++ // one empty row between tables
		}
		for ri, row := range tb {
			r++
			if r > maxRows {
				return "", fmt.Errorf("more than %d rows", maxRows)
			}
			if len(row) > maxCols {
				return "", fmt.Errorf("row %d has more than %d columns", r, maxCols)
			}
			fmt.Fprintf(&rows, `<row r="%d">`, r)
			for ci, v := range row {
				if v == "" {
					continue
				}
				ref := colName(ci) + strconv.Itoa(r)
				style := ""
				if s := styleFor(v, ri == 0); s > 0 {
					style = ` s="` + strconv.Itoa(s) + `"`
				}
				switch {
				case isNumber(v):
					fmt.Fprintf(&rows, `<c r="%s"%s><v>%s</v></c>`, ref, style, v)
				case strings.HasPrefix(v, "=") && len(v) > 1:
					fmt.Fprintf(&rows, `<c r="%s"%s><f>%s</f></c>`, ref, style, esc(v[1:]))
				default:
					fmt.Fprintf(&rows, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, esc(v))
				}
				if n := utf8.RuneCountInString(v); n > widths[ci] {
					widths[ci] = n
				}
			}
			rows.WriteString(`</row>`)
		}
	}
	var b strings.Builder
	b.WriteString(xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(widths) > 0 {
		maxCol := 0
		for c := range widths {
			maxCol = max(maxCol, c)
		}
		b.WriteString(`<cols>`)
		for c := 0; c <= maxCol; c++ {
			w := min(max(widths[c]+2, 8), 60)
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, c+1, c+1, w)
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>` + rows.String() + `</sheetData></worksheet>`)
	return b.String(), nil
}

// isNumber: a plain decimal that Excel stores without losing digits (15
// significant digits), so ids like long serial numbers stay text.
func isNumber(v string) bool {
	if !numberRe.MatchString(v) {
		return false
	}
	digits := 0
	for _, c := range strings.SplitN(strings.ToLower(v), "e", 2)[0] {
		if c >= '0' && c <= '9' {
			digits++
		}
	}
	return digits <= 15
}

// colName: 0 -> A, 25 -> Z, 26 -> AA.
func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// sheetNames returns valid, unique sheet names: Excel forbids []:*?/\, a
// leading/trailing apostrophe, names over 31 characters and duplicates
// (case-insensitively).
func sheetNames(sheets []Sheet) []string {
	clean := strings.NewReplacer("[", "(", "]", ")", ":", "-", "*", "-", "?", "", "/", "-", `\`, "-")
	seen := map[string]bool{}
	out := make([]string, len(sheets))
	for i, s := range sheets {
		name := strings.Trim(strings.TrimSpace(clean.Replace(s.Name)), "'")
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		base := truncRunes(name, maxSheetName)
		name = base
		for n := 2; seen[strings.ToLower(name)]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			name = truncRunes(base, maxSheetName-len(suffix)) + suffix
		}
		seen[strings.ToLower(name)] = true
		out[i] = name
	}
	return out
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// esc escapes text for XML and drops characters XML 1.0 can't carry.
func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF:
			b.WriteRune(r)
		}
	}
	return b.String()
}
