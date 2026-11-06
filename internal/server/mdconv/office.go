package mdconv

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// --- PPTX: one "## Slide N" section per slide, a:p paragraphs of a:t runs. ---

var slideRE = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func pptxMD(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	var nums []int
	for n := range z {
		if m := slideRE.FindStringSubmatch(n); m != nil {
			i, _ := strconv.Atoi(m[1])
			nums = append(nums, i)
		}
	}
	if len(nums) == 0 {
		return "", fmt.Errorf("no slides (not a PPTX file)")
	}
	sort.Ints(nums)
	var sb strings.Builder
	for _, n := range nums {
		b, err := readPart(z, fmt.Sprintf("ppt/slides/slide%d.xml", n))
		if err != nil {
			return "", err
		}
		paras, err := xmlParas(b, "p", "t")
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "## Slide %d\n\n%s\n\n", n, strings.Join(paras, "\n\n"))
	}
	return strings.TrimSpace(sb.String()), nil
}

// xmlParas collects, per <para> element, the concatenated text of <run> elements.
func xmlParas(b []byte, para, run string) ([]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var out []string
	var cur strings.Builder
	in := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			in = t.Name.Local == run
		case xml.EndElement:
			in = false
			if t.Name.Local == para {
				if s := strings.TrimSpace(cur.String()); s != "" {
					out = append(out, s)
				}
				cur.Reset()
			}
		case xml.CharData:
			if in {
				cur.Write(t)
			}
		}
	}
}

// --- XLSX: one "## Sheet" section per worksheet, rendered as a table. ---

func xlsxMD(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	// Shared strings: each <si> is one string made of <t> runs.
	var shared []string
	if b, err := readPart(z, "xl/sharedStrings.xml"); err == nil {
		shared, _ = xmlParas(b, "si", "t")
	}
	wb, err := readPart(z, "xl/workbook.xml")
	if err != nil {
		return "", fmt.Errorf("no workbook (not an XLSX file)")
	}
	rels, _ := readPart(z, "xl/_rels/workbook.xml.rels")
	targets := map[string]string{}
	var rd struct {
		R []struct{ ID, Target string } `xml:"Relationship"`
	}
	if xml.Unmarshal(rels, &rd) == nil {
		for _, r := range rd.R {
			// Targets are relative to xl/, or absolute from the package root.
			if t, abs := strings.CutPrefix(r.Target, "/"); abs {
				targets[r.ID] = t
			} else {
				targets[r.ID] = path.Join("xl", r.Target)
			}
		}
	}
	var wd struct {
		Sheets []struct {
			Name  string `xml:"name,attr"`
			State string `xml:"state,attr"`
			RID   string `xml:"id,attr"` // r:id; the namespace prefix is dropped by Local match
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(wb, &wd); err != nil {
		return "", err
	}
	n := len(wd.Sheets)
	var body strings.Builder
	var index []string
	anyData := false
	for i, sh := range wd.Sheets {
		name := strings.TrimSpace(sh.Name)
		if name == "" {
			name = fmt.Sprintf("Sheet%d", i+1)
		}
		p := targets[sh.RID]
		if p == "" {
			p = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		var rows [][]string
		note := ""
		if b, err := readPart(z, p); err != nil {
			note = "unreadable"
		} else if rows, err = sheetRows(b, shared); err != nil {
			rows, note = nil, "unreadable"
		} else if len(rows) == 0 {
			note = "empty"
		}
		if sh.State == "hidden" || sh.State == "veryHidden" {
			note = strings.TrimPrefix(note+", hidden", ", ")
		}
		// Keep every sheet visible in the output, even empty ones, so the
		// workbook's structure survives conversion.
		if n > 1 {
			fmt.Fprintf(&body, "## Sheet %d of %d: %s\n\n", i+1, n, name)
		} else {
			fmt.Fprintf(&body, "## %s\n\n", name)
		}
		if len(rows) == 0 {
			fmt.Fprintf(&body, "_(%s)_\n\n", note)
			index = append(index, fmt.Sprintf("%d. %s (%s)", i+1, name, note))
			continue
		}
		anyData = true
		cols := 0
		for _, r := range rows {
			cols = max(cols, len(r))
		}
		desc := fmt.Sprintf("%d rows × %d columns", len(rows)-1, cols)
		if note != "" {
			desc += ", " + note
		}
		index = append(index, fmt.Sprintf("%d. %s (%s)", i+1, name, desc))
		fmt.Fprintf(&body, "_%s; first row is the header._\n\n", desc)
		// Long sheets are cut into blocks that each restate the sheet name and
		// header row: chunking has no heading context, so a chunk from the
		// middle of a table would otherwise not say which sheet or columns it is.
		hdr, rest := rows[0], rows[1:]
		for off := 0; off < len(rest) || off == 0; off += sheetBlockRows {
			if off > 0 {
				fmt.Fprintf(&body, "### %s (continued, rows %d–%d)\n\n", name, off+1, min(off+sheetBlockRows, len(rest)))
			}
			blk := append([][]string{hdr}, rest[off:min(off+sheetBlockRows, len(rest))]...)
			body.WriteString(mdTable(blk) + "\n")
		}
	}
	if !anyData {
		return "", nil // all sheets empty: Convert reports "no extractable text"
	}
	var sb strings.Builder
	if n > 1 {
		fmt.Fprintf(&sb, "Workbook with %d sheets:\n\n%s\n\n", n, strings.Join(index, "\n"))
	}
	sb.WriteString(body.String())
	return strings.TrimSpace(sb.String()), nil
}

// sheetBlockRows is how many data rows go under one repeated header.
const sheetBlockRows = 40

// sheetRows reads worksheet cells into rows, placing each by its A1 reference
// so blank cells keep their column. Dates arrive as raw serial numbers
// (shortcut: number formats are not applied; upgrade if date columns matter).
func sheetRows(b []byte, shared []string) ([][]string, error) {
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var rows [][]string
	var row []string
	var col int
	var typ, text string
	var inV, inT bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				typ, text = "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						typ = a.Value
					case "r":
						col = colIndex(a.Value)
					}
				}
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				v := text
				if typ == "s" {
					if n, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && n >= 0 && n < len(shared) {
						v = shared[n]
					}
				}
				for len(row) <= col {
					row = append(row, "")
				}
				row[col] = v
			case "row":
				if strings.Join(row, "") != "" {
					rows = append(rows, row)
				}
			}
		case xml.CharData:
			if inV || inT {
				text += string(t)
			}
		}
	}
}

// colIndex turns an A1 reference's letters into a 0-based column ("C5" -> 2).
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A') + 1
	}
	return max(n-1, 0)
}

// --- EPUB: spine order from the OPF, each XHTML chapter through the HTML converter. ---

func epubMD(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	cb, err := readPart(z, "META-INF/container.xml")
	if err != nil {
		return "", fmt.Errorf("no container.xml (not an EPUB file)")
	}
	var c struct {
		Root struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(cb, &c); err != nil || c.Root.Path == "" {
		return "", fmt.Errorf("bad container.xml")
	}
	ob, err := readPart(z, c.Root.Path)
	if err != nil {
		return "", err
	}
	var o struct {
		Items []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(ob, &o); err != nil {
		return "", err
	}
	href := map[string]string{}
	for _, it := range o.Items {
		href[it.ID] = it.Href
	}
	dir := path.Dir(c.Root.Path)
	var parts []string
	for _, ref := range o.Spine {
		h, err := url.PathUnescape(href[ref.IDRef])
		if err != nil || h == "" {
			continue
		}
		b, err := readPart(z, path.Join(dir, h))
		if err != nil {
			continue
		}
		if md, _ := htmlMD(b); md != "" {
			parts = append(parts, md)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// --- ODT: content.xml headings, paragraphs, list items and tables. ---

func odtMD(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	b, err := readPart(z, "content.xml")
	if err != nil {
		return "", fmt.Errorf("no content.xml (not an ODT file)")
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var blocks []string
	var cur strings.Builder
	level, listDepth := 0, 0
	var table [][]string // non-nil while inside a table
	inTable, inCell := false, false
	var cell strings.Builder
	// text goes to the open cell, else the open block.
	w := func(s string) {
		if inCell {
			cell.WriteString(s)
		} else {
			cur.WriteString(s)
		}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "h":
				level = 1
				for _, a := range t.Attr {
					if a.Name.Local == "outline-level" {
						if n, _ := strconv.Atoi(a.Value); n > 0 {
							level = min(n, 6)
						}
					}
				}
			case "list":
				listDepth++
			case "table":
				inTable, table = true, [][]string{}
			case "table-row":
				if inTable {
					table = append(table, nil)
				}
			case "table-cell":
				if inTable {
					inCell = true
					cell.Reset()
				}
			case "tab":
				w("\t")
			case "line-break":
				w(" ")
			case "s":
				w(" ")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "h", "p":
				if inCell {
					cell.WriteByte(' ')
					break
				}
				if s := strings.TrimSpace(cur.String()); s != "" {
					switch {
					case t.Name.Local == "h":
						s = strings.Repeat("#", level) + " " + s
					case listDepth > 0:
						s = strings.Repeat("  ", listDepth-1) + "- " + s
					}
					blocks = append(blocks, s)
				}
				cur.Reset()
			case "list":
				listDepth--
			case "table-cell":
				if inCell {
					table[len(table)-1] = append(table[len(table)-1], cell.String())
					inCell = false
				}
			case "table":
				blocks = append(blocks, strings.TrimSpace(mdTable(table)))
				inTable = false
			}
		case xml.CharData:
			w(string(t))
		}
	}
	// Lists read better tight; everything else is blank-line separated.
	var sb strings.Builder
	for i, bl := range blocks {
		if i > 0 {
			if strings.HasPrefix(strings.TrimLeft(bl, " "), "- ") && strings.HasPrefix(strings.TrimLeft(blocks[i-1], " "), "- ") {
				sb.WriteString("\n")
			} else {
				sb.WriteString("\n\n")
			}
		}
		sb.WriteString(bl)
	}
	return sb.String(), nil
}
