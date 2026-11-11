// Package docxedit edits a .docx file in place: replace, insert and append at
// paragraph level. Only the bytes of word/document.xml the edit needs are
// spliced; every other byte and every other zip entry is copied unchanged, so
// fonts, styles, headers, numbering, images and page setup survive. The XML is
// never re-serialised, only tokenised to find byte ranges.
package docxedit

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Kind selects the operation.
type Kind int

const (
	Replace Kind = iota + 1 // Old -> New (ReplaceAll for every occurrence)
	Insert                  // Text after (or, with Before, in front of) the paragraph holding Anchor
	Append                  // Text at the end of the body
)

// Op is one edit. Passages (Old, Anchor) are quoted as read_doc shows them.
type Op struct {
	Kind       Kind
	Old, New   string
	ReplaceAll bool
	Text       string
	Anchor     string
	Before     bool
}

const maxXML = 64 << 20

// Edit applies op to the .docx in data and returns the new file and a short
// description of what was done. On any problem nothing is returned but the error.
func Edit(data []byte, op Op) (out []byte, desc string, err error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("the file is not a readable .docx (not a zip archive)")
	}
	var docFile *zip.File
	var styles []byte
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			docFile = f
		case "word/styles.xml":
			if styles, err = readEntry(f); err != nil {
				return nil, "", err
			}
		}
	}
	if docFile == nil {
		return nil, "", fmt.Errorf("the file is not a readable .docx (no word/document.xml)")
	}
	xmlData, err := readEntry(docFile)
	if err != nil {
		return nil, "", err
	}
	d, err := parse(xmlData)
	if err != nil {
		return nil, "", fmt.Errorf("word/document.xml is not readable: %w", err)
	}
	if d.tracked {
		return nil, "", fmt.Errorf("the document contains tracked changes (insertions or deletions); accept or reject them in Word first, then ask again")
	}
	if d.prefix == "" || d.bodyEnd < 0 {
		return nil, "", fmt.Errorf("word/document.xml has an unsupported structure")
	}
	b := &builder{data: xmlData, d: d, pfx: d.prefix + ":"}
	b.headings(styles)
	var newXML []byte
	var expected []string
	switch op.Kind {
	case Replace:
		newXML, expected, desc, err = b.replace(op)
	case Insert:
		newXML, expected, desc, err = b.insert(op)
	case Append:
		newXML, expected, desc, err = b.appendText(op)
	default:
		err = fmt.Errorf("unsupported operation")
	}
	if err != nil {
		return nil, "", err
	}
	if err := verify(newXML, expected); err != nil {
		return nil, "", fmt.Errorf("internal check failed, nothing was changed: %w", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		if f == docFile {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified})
			if err != nil {
				return nil, "", err
			}
			if _, err := w.Write(newXML); err != nil {
				return nil, "", err
			}
			continue
		}
		if err := zw.Copy(f); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), desc, nil
}

func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxXML+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxXML {
		return nil, fmt.Errorf("%s is too large to edit", f.Name)
	}
	return b, nil
}

// verify is the guard against handing back a corrupted file: the output must be
// well-formed and its paragraphs must be exactly the expected texts, in order.
func verify(newXML []byte, expected []string) error {
	d, err := parse(newXML)
	if err != nil {
		return err
	}
	if len(d.paras) != len(expected) {
		return fmt.Errorf("expected %d paragraphs, found %d", len(expected), len(d.paras))
	}
	for i, p := range d.paras {
		if string(p.text) != expected[i] {
			return fmt.Errorf("paragraph %d is %q, expected %q", i+1, snippet(string(p.text)), snippet(expected[i]))
		}
	}
	return nil
}

func snippet(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

type edit struct {
	start, end int
	repl       string
}

func splice(data []byte, edits []edit) ([]byte, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	pos := 0
	for _, e := range edits {
		if e.start < pos || e.end < e.start {
			return nil, fmt.Errorf("internal error: overlapping edits")
		}
		out.Write(data[pos:e.start])
		out.WriteString(e.repl)
		pos = e.end
	}
	out.Write(data[pos:])
	return out.Bytes(), nil
}

type builder struct {
	data       []byte
	d          *document
	pfx        string
	headingID  map[int]string // heading level -> style id, by style name
	headingSet map[string]bool
}

var headingNameRE = regexp.MustCompile(`^heading ([1-9])$`)

// headings finds the heading styles by their built-in names ("heading 1"); the
// style ids are language dependent.
func (b *builder) headings(styles []byte) {
	b.headingID, b.headingSet = map[int]string{}, map[string]bool{}
	dec := xml.NewDecoder(bytes.NewReader(styles))
	id, inStyle := "", false
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !isW(t.Name) {
				continue
			}
			switch t.Name.Local {
			case "style":
				id, inStyle = attr(t, "styleId"), true
			case "name":
				if m := headingNameRE.FindStringSubmatch(strings.ToLower(attr(t, "val"))); inStyle && m != nil && id != "" {
					lvl := int(m[1][0] - '0')
					if _, dup := b.headingID[lvl]; !dup {
						b.headingID[lvl] = id
					}
					b.headingSet[id] = true
				}
			}
		case xml.EndElement:
			if isW(t.Name) && t.Name.Local == "style" {
				inStyle = false
			}
		}
	}
}

func (b *builder) isHeading(p *para) bool { return p.style != "" && b.headingSet[p.style] }

func (b *builder) tag(name string) string { return b.pfx + name }
