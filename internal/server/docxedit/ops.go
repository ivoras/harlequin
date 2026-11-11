package docxedit

import (
	"fmt"
	"regexp"
	"strings"
)

type query struct {
	text  []rune   // normalized passage (a paragraph match)
	cells [][]rune // normalized cells (a table-row match)
	isRow bool
}

// prepareQuery turns a passage quoted from read_doc into its normalized form:
// Markdown marks removed, whitespace collapsed. A quoted table row ("| a | b |")
// becomes its cells; a single "| a |" cell is plain text.
func prepareQuery(q, name string) (query, error) {
	var lines []string
	pipes := 0
	for _, l := range strings.Split(strings.ReplaceAll(q, "\r\n", "\n"), "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
			if strings.HasPrefix(t, "|") {
				pipes++
			}
		}
	}
	if len(lines) == 0 {
		return query{}, fmt.Errorf("%s must not be empty", name)
	}
	var out query
	if pipes > 0 {
		var rows [][]string
		for _, l := range lines {
			if c := splitRow(l); strings.HasPrefix(l, "|") && !isSepRow(c) {
				rows = append(rows, c)
			}
		}
		if pipes != len(lines) || len(rows) != 1 {
			return out, fmt.Errorf("%s: quote one table row, or text from one cell or paragraph", name)
		}
		if len(rows[0]) > 1 {
			out.isRow = true
			for _, c := range rows[0] {
				n, _ := normalize([]rune(c))
				out.cells = append(out.cells, n)
			}
			return out, nil
		}
		lines = rows[0]
	}
	for i := range lines {
		lines[i] = stripMarkdown(lines[i])
	}
	out.text, _ = normalize([]rune(strings.Join(lines, " ")))
	if len(out.text) == 0 {
		return out, fmt.Errorf("%s must contain some text of the document", name)
	}
	return out, nil
}

func indexRunes(hay, needle []rune, from int) int {
	for i := from; i+len(needle) <= len(hay); i++ {
		j := 0
		for j < len(needle) && hay[i+j] == needle[j] {
			j++
		}
		if j == len(needle) {
			return i
		}
	}
	return -1
}

type match struct {
	p    *para
	pi   int
	s, e int // rune range in p.text
}

// findParas returns every occurrence of the passage inside a paragraph.
func (b *builder) findParas(q query) []match {
	var ms []match
	for pi, p := range b.d.paras {
		n, orig := normalize(p.text)
		for from := 0; ; {
			i := indexRunes(n, q.text, from)
			if i < 0 {
				break
			}
			last := i + len(q.text) - 1
			ms = append(ms, match{p, pi, orig[i], orig[last] + 1})
			from = i + len(q.text)
		}
	}
	return ms
}

// findRows returns the table rows whose cells contain the quoted cells in order.
func (b *builder) findRows(q query) []int {
	var rows []int
	for ri := range b.d.trs {
		var cells [][]rune
		textbox := false
		for ti, tc := range b.d.tcs {
			if tc.tr != ri {
				continue
			}
			var parts []rune
			for _, pi := range b.d.tcs[ti].paras {
				p := b.d.paras[pi]
				textbox = textbox || p.textbox
				parts = append(append(parts, p.text...), ' ')
			}
			n, _ := normalize(parts)
			cells = append(cells, n)
		}
		if textbox {
			continue
		}
		j, ok := 0, true
		for _, qc := range q.cells {
			for j < len(cells) && indexRunes(cells[j], qc, 0) < 0 {
				j++
			}
			if j == len(cells) {
				ok = false
				break
			}
			j++
		}
		if ok {
			rows = append(rows, ri)
		}
	}
	return rows
}

const notFoundHint = " (matching ignores line breaks, extra spaces and Markdown marks such as ** and #, but is otherwise exact, and the passage must lie within one paragraph); copy it as read_doc shows it"

// outsideTextBoxes drops matches inside text boxes unless every match is one,
// in which case the edit is refused.
func outsideTextBoxes(ms []match) ([]match, error) {
	var keep []match
	for _, m := range ms {
		if !m.p.textbox {
			keep = append(keep, m)
		}
	}
	if len(keep) == 0 && len(ms) > 0 {
		return nil, fmt.Errorf("the passage is inside a text box, which cannot be edited in place; insert a new paragraph next to text outside the box instead")
	}
	return keep, nil
}

func (b *builder) replace(op Op) ([]byte, []string, string, error) {
	if op.Old == "" {
		return nil, nil, "", fmt.Errorf("old_string must not be empty (to add text use insert or append)")
	}
	if op.Old == op.New {
		return nil, nil, "", fmt.Errorf("old_string and new_string are identical; nothing to change")
	}
	if strings.ContainsAny(op.New, "\r\n") {
		return nil, nil, "", fmt.Errorf("new_string must be a single line of text; to add a paragraph use insert with after or before")
	}
	q, err := prepareQuery(op.Old, "old_string")
	if err != nil {
		return nil, nil, "", err
	}
	if q.isRow {
		return nil, nil, "", fmt.Errorf("old_string spans several table cells; replace the text of one cell at a time")
	}
	ms, err := outsideTextBoxes(b.findParas(q))
	if err != nil {
		return nil, nil, "", err
	}
	switch {
	case len(ms) == 0:
		return nil, nil, "", fmt.Errorf("old_string was not found%s", notFoundHint)
	case len(ms) > 1 && !op.ReplaceAll:
		return nil, nil, "", fmt.Errorf("old_string occurs %d times; include more surrounding text to make it unique, or set replace_all to replace every occurrence", len(ms))
	}
	byPara := map[int][]match{}
	for _, m := range ms {
		byPara[m.pi] = append(byPara[m.pi], m)
	}
	var edits []edit
	var expected []string
	removed := 0
	for pi, p := range b.d.paras {
		pm := byPara[pi]
		if len(pm) == 0 {
			expected = append(expected, string(p.text))
			continue
		}
		es, text, gone, err := b.rewritePara(p, pm, op.New)
		if err != nil {
			return nil, nil, "", err
		}
		edits = append(edits, es...)
		if gone {
			removed++
		} else {
			expected = append(expected, text)
		}
	}
	out, err := splice(b.data, edits)
	if err != nil {
		return nil, nil, "", err
	}
	desc := fmt.Sprintf("replaced %d occurrence(s) in place", len(ms))
	if removed > 0 {
		desc += fmt.Sprintf(", removing %d emptied paragraph(s)", removed)
	}
	return out, expected, desc, nil
}

// rewritePara applies the matches of one paragraph. The new text goes into the
// run the match starts in (its formatting applies), the matched text is removed
// from the other runs, and runs left empty are dropped.
func (b *builder) rewritePara(p *para, ms []match, newStr string) (edits []edit, text string, gone bool, err error) {
	atomNew := make([][]rune, len(p.atoms))
	touched := make([]bool, len(p.atoms))
	for i, a := range p.atoms {
		for j, r := range a.text {
			pos := p.atomOff[i] + j
			mi := -1
			for k, m := range ms {
				if pos >= m.s && pos < m.e {
					mi = k
				}
			}
			switch {
			case mi < 0:
				atomNew[i] = append(atomNew[i], r)
			default:
				touched[i] = true
				if pos == ms[mi].s {
					atomNew[i] = append(atomNew[i], []rune(newStr)...)
				}
			}
		}
		if touched[i] && a.protected {
			return nil, "", false, fmt.Errorf("the passage lies inside a hyperlink or a field, which cannot be edited in place; insert a new paragraph next to it instead")
		}
		text += string(atomNew[i])
	}
	dropped := make([]bool, len(p.runs))
	droppable := true
	for ri, r := range p.runs {
		if r.other {
			droppable = false
			continue
		}
		any, all := false, true
		for _, ai := range r.atoms {
			any = any || touched[ai]
			all = all && len(atomNew[ai]) == 0
		}
		dropped[ri] = len(r.atoms) > 0 && any && all
	}
	isSect := false
	for _, k := range p.pKids {
		isSect = isSect || k.name == "sectPr"
	}
	soleInCell := p.tc >= 0 && len(b.d.tcs[p.tc].paras) <= 1
	if newStr == "" && text == "" && droppable && !p.hasTextbox && !isSect && !soleInCell {
		return []edit{{p.start, p.end, ""}}, "", true, nil
	}
	for ri, r := range p.runs {
		if dropped[ri] {
			edits = append(edits, edit{r.start, r.end, ""})
		}
	}
	for i, a := range p.atoms {
		if !touched[i] || dropped[a.run] {
			continue
		}
		repl := ""
		if len(atomNew[i]) > 0 {
			qn := a.qname
			if a.kind != 't' {
				qn = b.tag("t")
			}
			repl = fmt.Sprintf(`<%s xml:space="preserve">%s</%s>`, qn, esc(string(atomNew[i])), qn)
		}
		edits = append(edits, edit{a.start, a.end, repl})
	}
	return edits, text, false, nil
}

func (b *builder) insert(op Op) ([]byte, []string, string, error) {
	name := "after"
	if op.Before {
		name = "before"
	}
	blocks, row, err := parseBlocks(op.Text)
	if err != nil {
		return nil, nil, "", err
	}
	if len(blocks) == 0 && row == nil {
		return nil, nil, "", fmt.Errorf("insert must not be empty")
	}
	q, err := prepareQuery(op.Anchor, name)
	if err != nil {
		return nil, nil, "", err
	}
	var p *para
	tr := -1
	if q.isRow {
		rows := b.findRows(q)
		switch {
		case len(rows) == 0:
			return nil, nil, "", fmt.Errorf("%s: no table row has these cells in this order%s", name, notFoundHint)
		case len(rows) > 1:
			return nil, nil, "", fmt.Errorf("%s matches %d table rows; include more cell text to make it unique", name, len(rows))
		}
		if row == nil {
			return nil, nil, "", fmt.Errorf("%s names a table row; to add next to it insert a row such as | a | b |, or quote the text of one cell to add a paragraph in that cell", name)
		}
		tr = rows[0]
	} else {
		ms, err := outsideTextBoxes(b.findParas(q))
		if err != nil {
			return nil, nil, "", err
		}
		switch {
		case len(ms) == 0:
			return nil, nil, "", fmt.Errorf("%s was not found%s", name, notFoundHint)
		case len(ms) > 1:
			return nil, nil, "", fmt.Errorf("%s occurs %d times; include more surrounding text to make it unique", name, len(ms))
		}
		p = ms[0].p
		if row != nil {
			if p.tr < 0 {
				return nil, nil, "", fmt.Errorf("a table row can only be inserted next to a row of an existing table; %s is not in a table, so write plain text to add a paragraph", name)
			}
			tr = p.tr
		}
	}
	var off int
	var xmlOut string
	var texts []string
	var what string
	if row != nil {
		xmlOut, texts, err = b.cloneRow(tr, row)
		if err != nil {
			return nil, nil, "", err
		}
		off = b.d.trs[tr].end
		if op.Before {
			off = b.d.trs[tr].start
		}
		what = fmt.Sprintf("inserted 1 table row %s the row starting %q", name, snippet(rowStart(b, tr)))
	} else {
		src := p
		if b.isHeading(p) {
			src = b.nearestBody(p, !op.Before)
		}
		xmlOut, texts = b.paragraphs(blocks, src)
		off = p.end
		if op.Before {
			off = p.start
		}
		what = fmt.Sprintf("inserted %d paragraph(s) %s the paragraph starting %q", len(texts), name, snippet(string(p.text)))
	}
	return b.splicePara(off, xmlOut, texts, what)
}

func rowStart(b *builder, tr int) string {
	var sb strings.Builder
	for _, tc := range b.d.tcs {
		if tc.tr == tr {
			for _, pi := range tc.paras {
				sb.WriteString(string(b.d.paras[pi].text) + " ")
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// splicePara inserts xmlOut at byte offset off and builds the expected
// paragraph list: the original texts with the new ones at that position.
func (b *builder) splicePara(off int, xmlOut string, texts []string, what string) ([]byte, []string, string, error) {
	out, err := splice(b.data, []edit{{off, off, xmlOut}})
	if err != nil {
		return nil, nil, "", err
	}
	var expected []string
	done := false
	for _, p := range b.d.paras {
		if !done && p.start >= off {
			expected, done = append(expected, texts...), true
		}
		expected = append(expected, string(p.text))
	}
	if !done {
		expected = append(expected, texts...)
	}
	return out, expected, what, nil
}

func (b *builder) appendText(op Op) ([]byte, []string, string, error) {
	blocks, row, err := parseBlocks(op.Text)
	if err != nil {
		return nil, nil, "", err
	}
	if row != nil {
		return nil, nil, "", fmt.Errorf("a table row cannot be appended at the end of the document; use insert with after or before next to a row of an existing table")
	}
	if len(blocks) == 0 {
		return nil, nil, "", fmt.Errorf("append must not be empty")
	}
	var src *para
	for _, p := range b.d.paras {
		if p.inBody && !p.textbox && len(p.text) > 0 && !b.isHeading(p) {
			src = p
		}
	}
	off := b.d.sectStart
	if off < 0 {
		off = b.d.bodyEnd
	}
	xmlOut, texts := b.paragraphs(blocks, src)
	return b.splicePara(off, xmlOut, texts, fmt.Sprintf("appended %d paragraph(s) at the end of the document", len(texts)))
}

// nearestBody finds the nearest non-heading paragraph in the direction of the
// insertion (forward for after), in the same kind of place (table or body).
func (b *builder) nearestBody(p *para, forward bool) *para {
	idx := 0
	for i, c := range b.d.paras {
		if c == p {
			idx = i
		}
	}
	for i := idx; i >= 0 && i < len(b.d.paras); {
		if forward {
			i++
		} else {
			i--
		}
		if i < 0 || i >= len(b.d.paras) {
			break
		}
		if c := b.d.paras[i]; !c.textbox && !b.isHeading(c) && len(c.text) > 0 && (c.tc >= 0) == (p.tc >= 0) {
			return c
		}
	}
	return nil
}

var dropFromPPr = map[string]bool{"sectPr": true, "pageBreakBefore": true, "pPrChange": true}

// pPrOf copies a paragraph's properties (minus the ones that must not be
// duplicated: a section break, a page break before).
func pPrOf(data []byte, p *para) string {
	if p == nil || p.pPr[0] < 0 {
		return ""
	}
	var sb strings.Builder
	pos := p.pPr[0]
	for _, k := range p.pKids {
		if dropFromPPr[k.name] {
			sb.Write(data[pos:k.start])
			pos = k.end
		}
	}
	sb.Write(data[pos:p.pPr[1]])
	return sb.String()
}

type kidRaw struct{ name, raw string }

// rPrOf returns the run properties of the paragraph's first ordinary run.
func rPrOf(data []byte, p *para) []kidRaw {
	if p == nil {
		return nil
	}
	for _, r := range p.runs {
		if r.rPr[0] < 0 || r.other || (len(r.atoms) > 0 && p.atoms[r.atoms[0]].protected) {
			continue
		}
		raw := data[r.rPr[0]:r.rPr[1]]
		cs, _, _ := kids(raw)
		var out []kidRaw
		for _, c := range cs {
			out = append(out, kidRaw{c.name, string(raw[c.start:c.end])})
		}
		return out
	}
	return nil
}

// rPrXML builds <w:rPr> from copied children with bold/italic forced on.
func (b *builder) rPrXML(base []kidRaw, bold, italic bool) string {
	var lead, bs, is, rest []string
	for _, k := range base {
		switch k.name {
		case "rStyle", "rFonts":
			lead = append(lead, k.raw)
		case "b", "bCs":
			bs = append(bs, k.raw)
		case "i", "iCs":
			is = append(is, k.raw)
		default:
			rest = append(rest, k.raw)
		}
	}
	if bold {
		bs = []string{"<" + b.tag("b") + "/>"}
	}
	if italic {
		is = []string{"<" + b.tag("i") + "/>"}
	}
	all := strings.Join(lead, "") + strings.Join(bs, "") + strings.Join(is, "") + strings.Join(rest, "")
	if all == "" {
		return ""
	}
	return "<" + b.tag("rPr") + ">" + all + "</" + b.tag("rPr") + ">"
}

func (b *builder) runsXML(text string, base []kidRaw, forceBold bool) (string, string) {
	var sb strings.Builder
	ss := spans(text)
	for _, s := range ss {
		if s.text == "" {
			continue
		}
		t := b.tag("t")
		fmt.Fprintf(&sb, "<%s>%s<%s xml:space=\"preserve\">%s</%s></%s>", b.tag("r"), b.rPrXML(base, s.bold || forceBold, s.italic), t, esc(s.text), t, b.tag("r"))
	}
	return sb.String(), plain(ss)
}

// paragraphs builds the new paragraphs, styled like src.
func (b *builder) paragraphs(blocks []block, src *para) (string, []string) {
	pPr := pPrOf(b.data, src)
	base := rPrOf(b.data, src)
	list := src != nil && src.hasNum()
	var sb strings.Builder
	var texts []string
	for _, bl := range blocks {
		ppr, rbase, bold, text := pPr, base, false, bl.text
		switch bl.kind {
		case 'h':
			if id := b.headingID[bl.level]; id != "" {
				ppr, rbase = fmt.Sprintf(`<%s><%s %s:val="%s"/></%s>`, b.tag("pPr"), b.tag("pStyle"), b.pfx[:len(b.pfx)-1], strings.ReplaceAll(esc(id), `"`, "&quot;"), b.tag("pPr")), nil
			} else {
				bold = true
			}
		case 'l':
			if !list {
				text = bl.raw
			}
		}
		runs, plainText := b.runsXML(text, rbase, bold)
		fmt.Fprintf(&sb, "<%s>%s%s</%s>", b.tag("p"), ppr, runs, b.tag("p"))
		texts = append(texts, plainText)
	}
	return sb.String(), texts
}

var tblHeaderRE = regexp.MustCompile(`<[A-Za-z0-9_]+:tblHeader[^>]*/>`)

// cloneRow copies table row tr with new cell texts, keeping the row's and the
// cells' properties and each cell's first paragraph style.
func (b *builder) cloneRow(tr int, cells []string) (string, []string, error) {
	frag := b.data[b.d.trs[tr].start:b.d.trs[tr].end]
	fd, err := parse(frag)
	if err != nil {
		return "", nil, err
	}
	var direct []int
	for i, tc := range fd.tcs {
		if tc.tr == 0 {
			direct = append(direct, i)
		}
	}
	if len(direct) != len(cells) {
		return "", nil, fmt.Errorf("the table row to insert has %d cells but the row it goes next to has %d; give a row with %d cells", len(cells), len(direct), len(direct))
	}
	var sb strings.Builder
	var texts []string
	pos := 0
	for i, ti := range direct {
		tc := fd.tcs[ti]
		sb.Write(frag[pos:tc.start])
		sb.Write(frag[tc.start:tc.headEnd])
		var src *para
		if len(tc.paras) > 0 {
			src = fd.paras[tc.paras[0]]
		}
		fb := &builder{data: frag, d: fd, pfx: b.pfx}
		runs, text := fb.runsXML(cells[i], rPrOf(frag, src), false)
		fmt.Fprintf(&sb, "<%s>%s%s</%s>", b.tag("p"), pPrOf(frag, src), runs, b.tag("p"))
		texts = append(texts, text)
		sb.Write(frag[tc.closeStart:tc.end])
		pos = tc.end
	}
	sb.Write(frag[pos:])
	return tblHeaderRE.ReplaceAllString(sb.String(), ""), texts, nil
}
