package docxedit

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

const wNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"

// isW reports whether n is a WordprocessingML name. The bare "w" prefix is
// accepted so a fragment (a cloned table row) parses without its namespace
// declarations.
func isW(n xml.Name) bool { return n.Space == wNS || n.Space == "w" }

// The model below is only ever used to find byte ranges: every change is made
// by splicing bytes into the original XML, never by re-serialising it.

type atom struct { // one text-bearing child of a run
	kind       byte // 't' text, 'T' tab, 'B' line break
	start, end int  // byte range of the element
	qname      string
	text       []rune
	run        int
	protected  bool // inside a hyperlink or a field
}

type run struct {
	start, end int
	rPr        [2]int // byte range of its <w:rPr>, or {-1,-1}
	other      bool   // has content besides rPr/t/tab/br (drawing, field mark, ...)
	atoms      []int
}

type child struct {
	name       string
	start, end int
}

type para struct {
	start, end int
	pPr        [2]int
	pKids      []child
	style      string
	atoms      []atom
	runs       []run
	text       []rune
	atomOff    []int // rune offset of each atom in text
	textbox    bool  // inside a text box
	hasTextbox bool  // contains a text box
	tr, tc     int   // innermost table row / cell, -1 outside tables
	inBody     bool  // a direct child of <w:body>
}

type trInfo struct{ start, end int }

type tcInfo struct {
	start, end, headEnd, closeStart, tr int
	paras                               []int // indices of the paragraphs directly in this cell
}

type document struct {
	paras     []*para
	trs       []trInfo
	tcs       []tcInfo
	sectStart int // the body's final <w:sectPr>, or -1
	bodyEnd   int // start of </w:body>, or -1
	tracked   bool
	prefix    string // the prefix bound to the WordprocessingML namespace
}

func (p *para) hasNum() bool {
	for _, k := range p.pKids {
		if k.name == "numPr" {
			return true
		}
	}
	return false
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func qnameAt(data []byte, start int) string {
	i := start + 1
	j := i
	for j < len(data) && data[j] != ' ' && data[j] != '>' && data[j] != '/' && data[j] != '\t' && data[j] != '\n' && data[j] != '\r' {
		j++
	}
	return string(data[i:j])
}

// parse reads WordprocessingML (a whole document.xml or a fragment of it) into
// the paragraph model. It fails on malformed XML.
func parse(data []byte) (*document, error) {
	d := &document{sectStart: -1, bodyEnd: -1}
	dec := xml.NewDecoder(bytes.NewReader(data))
	type open struct {
		local string // "" for a non-WordprocessingML element
		start int
	}
	type runRef struct {
		p *para
		i int
	}
	type atomRef struct {
		p *para
		i int
	}
	var stack []open
	var paraStack []*para
	var runStack []runRef
	var trStack, tcStack []int
	var cur *atomRef
	hl, fs, fld, txbx := 0, 0, 0, 0
	for {
		prev := int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("malformed XML: %w", err)
		}
		now := int(dec.InputOffset())
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].local
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := ""
			if isW(t.Name) {
				local = t.Name.Local
			}
			if len(stack) == 0 {
				for _, a := range t.Attr {
					if a.Name.Space == "xmlns" && a.Value == wNS {
						d.prefix = a.Name.Local
					}
				}
			}
			stack = append(stack, open{local, prev})
			var curPara *para
			if len(paraStack) > 0 {
				curPara = paraStack[len(paraStack)-1]
			}
			switch local {
			case "p":
				p := &para{start: prev, pPr: [2]int{-1, -1}, tr: -1, tc: -1, textbox: txbx > 0, inBody: parent == "body"}
				if len(trStack) > 0 {
					p.tr = trStack[len(trStack)-1]
				}
				if len(tcStack) > 0 {
					p.tc = tcStack[len(tcStack)-1]
					d.tcs[p.tc].paras = append(d.tcs[p.tc].paras, len(d.paras))
				}
				if curPara != nil {
					curPara.hasTextbox = true
				}
				d.paras = append(d.paras, p)
				paraStack = append(paraStack, p)
			case "txbxContent":
				txbx++
			case "pPr":
				if parent == "p" && curPara != nil {
					curPara.pPr[0] = prev
				}
			case "pStyle":
				if parent == "pPr" && curPara != nil {
					curPara.style = attr(t, "val")
				}
			case "r":
				if curPara != nil {
					curPara.runs = append(curPara.runs, run{start: prev, rPr: [2]int{-1, -1}})
					runStack = append(runStack, runRef{curPara, len(curPara.runs) - 1})
				}
			case "rPr":
				if parent == "r" && len(runStack) > 0 {
					r := runStack[len(runStack)-1]
					r.p.runs[r.i].rPr[0] = prev
				}
			case "t", "tab", "br", "cr":
				if parent != "r" || len(runStack) == 0 {
					break
				}
				r := runStack[len(runStack)-1]
				if (local == "br") && (attr(t, "type") == "page" || attr(t, "type") == "column") {
					r.p.runs[r.i].other = true // a page break is content, but not text
					break
				}
				a := atom{start: prev, run: r.i, protected: hl > 0 || fs > 0 || fld > 0}
				switch local {
				case "t":
					a.kind, a.qname = 't', qnameAt(data, prev)
				case "tab":
					a.kind, a.text = 'T', []rune{'\t'}
				default:
					a.kind, a.text = 'B', []rune{'\n'}
				}
				r.p.atoms = append(r.p.atoms, a)
				r.p.runs[r.i].atoms = append(r.p.runs[r.i].atoms, len(r.p.atoms)-1)
				cur = &atomRef{r.p, len(r.p.atoms) - 1}
			case "hyperlink":
				hl++
			case "fldSimple":
				fs++
			case "fldChar":
				if parent == "r" && len(runStack) > 0 {
					r := runStack[len(runStack)-1]
					r.p.runs[r.i].other = true
				}
				switch attr(t, "fldCharType") {
				case "begin":
					fld++
				case "end":
					if fld > 0 {
						fld--
					}
				}
			case "tr":
				d.trs = append(d.trs, trInfo{start: prev})
				trStack = append(trStack, len(d.trs)-1)
			case "tc":
				tr := -1
				if len(trStack) > 0 {
					tr = trStack[len(trStack)-1]
				}
				d.tcs = append(d.tcs, tcInfo{start: prev, headEnd: now, tr: tr})
				tcStack = append(tcStack, len(d.tcs)-1)
			case "sectPr":
				if parent == "body" {
					d.sectStart = prev
				}
			case "ins", "del", "moveFrom", "moveTo":
				d.tracked = true
			}
			if parent == "r" && local != "rPr" && local != "t" && local != "tab" && local != "br" && local != "cr" && len(runStack) > 0 {
				r := runStack[len(runStack)-1]
				r.p.runs[r.i].other = true
			}
			if parent == "r" && local == "" && len(runStack) > 0 { // foreign element in a run (mc:AlternateContent, ...)
				r := runStack[len(runStack)-1]
				r.p.runs[r.i].other = true
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("malformed XML: unbalanced end tag")
			}
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pl := ""
			if len(stack) > 0 {
				pl = stack[len(stack)-1].local
			}
			var curPara *para
			if len(paraStack) > 0 {
				curPara = paraStack[len(paraStack)-1]
			}
			if pl == "pPr" && curPara != nil && o.local != "" {
				curPara.pKids = append(curPara.pKids, child{o.local, o.start, now})
			}
			switch o.local {
			case "p":
				if curPara != nil {
					curPara.end = now
					paraStack = paraStack[:len(paraStack)-1]
				}
			case "txbxContent":
				txbx--
			case "pPr":
				if pl == "p" && curPara != nil {
					curPara.pPr[1] = now
				}
			case "r":
				if len(runStack) > 0 {
					r := runStack[len(runStack)-1]
					r.p.runs[r.i].end = now
					runStack = runStack[:len(runStack)-1]
				}
			case "rPr":
				if pl == "r" && len(runStack) > 0 {
					r := runStack[len(runStack)-1]
					r.p.runs[r.i].rPr[1] = now
				}
			case "t", "tab", "br", "cr":
				if cur != nil && cur.p.atoms[cur.i].start == o.start {
					cur.p.atoms[cur.i].end = now
					cur = nil
				}
			case "hyperlink":
				hl--
			case "fldSimple":
				fs--
			case "tr":
				d.trs[trStack[len(trStack)-1]].end = now
				trStack = trStack[:len(trStack)-1]
			case "tc":
				i := tcStack[len(tcStack)-1]
				d.tcs[i].end, d.tcs[i].closeStart = now, prev
				tcStack = tcStack[:len(tcStack)-1]
			case "tcPr":
				if pl == "tc" {
					d.tcs[tcStack[len(tcStack)-1]].headEnd = now
				}
			case "body":
				d.bodyEnd = prev
			}
		case xml.CharData:
			if cur != nil && cur.p.atoms[cur.i].kind == 't' && len(stack) > 0 && stack[len(stack)-1].local == "t" {
				a := &cur.p.atoms[cur.i]
				a.text = append(a.text, []rune(string(t))...)
			}
		}
	}
	if len(stack) != 0 {
		return nil, fmt.Errorf("malformed XML: unclosed element")
	}
	for _, p := range d.paras {
		p.atomOff = make([]int, len(p.atoms))
		for i, a := range p.atoms {
			p.atomOff[i] = len(p.text)
			p.text = append(p.text, a.text...)
		}
	}
	return d, nil
}

// kids lists the direct children of the single root element in frag (an
// <w:pPr> or <w:rPr> copied out of the document).
func kids(frag []byte) (cs []child, openEnd, closeStart int) {
	dec := xml.NewDecoder(bytes.NewReader(frag))
	depth := 0
	var starts []int
	for {
		prev := int(dec.InputOffset())
		tok, err := dec.Token()
		if err != nil {
			return cs, openEnd, closeStart
		}
		now := int(dec.InputOffset())
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				openEnd = now
			}
			starts = append(starts, prev)
			_ = t
			depth++
			if depth == 2 {
				cs = append(cs, child{name: t.Name.Local, start: prev})
			}
		case xml.EndElement:
			depth--
			starts = starts[:len(starts)-1]
			if depth == 1 {
				cs[len(cs)-1].end = now
			}
			if depth == 0 {
				closeStart = prev
			}
		}
	}
}
