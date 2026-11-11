package docxedit

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

const nsDecl = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wps="http://schemas.microsoft.com/office/word/2010/wordprocessingShape" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"`

// Invented content, written the way Word writes it: runs split at proofing
// marks and bookmarks, rsid attributes, non-English style ids.
const fixtureBody = `<w:p w:rsidR="00A10001"><w:pPr><w:pStyle w:val="Naslov1"/></w:pPr><w:r><w:t>Rental agreement</w:t></w:r></w:p>` +
	`<w:p w:rsidR="00A10002"><w:pPr><w:pStyle w:val="Tijelo"/><w:jc w:val="both"/></w:pPr>` +
	`<w:r w:rsidRPr="00A10002"><w:rPr><w:rFonts w:ascii="Arial"/><w:sz w:val="22"/></w:rPr><w:t xml:space="preserve">Payment is due in </w:t></w:r>` +
	`<w:proofErr w:type="spellStart"/><w:bookmarkStart w:id="0" w:name="pay"/>` +
	`<w:r><w:rPr><w:rFonts w:ascii="Arial"/><w:b/><w:sz w:val="22"/></w:rPr><w:t>30</w:t></w:r><w:bookmarkEnd w:id="0"/><w:proofErr w:type="spellEnd"/>` +
	`<w:r><w:rPr><w:rFonts w:ascii="Arial"/><w:sz w:val="22"/></w:rPr><w:t xml:space="preserve"> days of the invoice date &amp; no later.</w:t></w:r></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Tijelo"/></w:pPr><w:r><w:t>Notice period: 30 days.</w:t></w:r></w:p>` +
	`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/></w:tblPr><w:tblGrid><w:gridCol w:w="4000"/><w:gridCol w:w="4000"/></w:tblGrid>` +
	`<w:tr><w:trPr><w:tblHeader/></w:trPr><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>Item</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:rPr><w:b/></w:rPr><w:t>Price</w:t></w:r></w:p></w:tc></w:tr>` +
	`<w:tr><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>Rent</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:tcW w:w="4000" w:type="dxa"/></w:tcPr><w:p><w:r><w:t>500 EUR</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
	`<w:p><w:pPr><w:pStyle w:val="Odlomak"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>First item</w:t></w:r></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Odlomak"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>Second item</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t xml:space="preserve">See </w:t></w:r><w:hyperlink r:id="rId5"><w:r><w:rPr><w:rStyle w:val="Hyperlink"/></w:rPr><w:t>the example link</w:t></w:r></w:hyperlink><w:r><w:t xml:space="preserve"> for details.</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t xml:space="preserve">Page </w:t></w:r><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>3</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r><w:r><w:t xml:space="preserve"> of the file.</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t>Label text</w:t></w:r><w:r><w:drawing><wps:wsp><wps:txbx><w:txbxContent><w:p><w:r><w:t>Boxed words</w:t></w:r></w:p></w:txbxContent></wps:txbx></wps:wsp></w:drawing></w:r></w:p>` +
	`<w:p><w:pPr><w:pStyle w:val="Tijelo"/></w:pPr><w:r><w:t>Cijena je 1.500 € — plaća se u roku.</w:t></w:r></w:p>` +
	`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440"/></w:sectPr>`

const fixtureStyles = `<?xml version="1.0" encoding="UTF-8"?><w:styles ` + nsDecl + `>` +
	`<w:style w:type="paragraph" w:styleId="Naslov1"><w:name w:val="heading 1"/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Naslov2"><w:name w:val="heading 2"/></w:style>` +
	`<w:style w:type="paragraph" w:styleId="Tijelo"><w:name w:val="Body Text"/></w:style></w:styles>`

func docxWith(t testing.TB, body, styles string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	add("[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`)
	add("_rels/.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x" Target="word/document.xml"/></Relationships>`)
	add("word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+"\n"+`<w:document `+nsDecl+`><w:body>`+body+`</w:body></w:document>`)
	if styles != "" {
		add("word/styles.xml", styles)
	}
	add("word/header1.xml", `<?xml version="1.0"?><w:hdr `+nsDecl+`><w:p><w:r><w:t>Header words</w:t></w:r></w:p></w:hdr>`)
	zw.Close()
	return buf.Bytes()
}

func fixture(t testing.TB) []byte { return docxWith(t, fixtureBody, fixtureStyles) }

func entries(t testing.TB, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		m[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	return m
}

func texts(t testing.TB, data []byte) []string {
	t.Helper()
	d, err := parse(entries(t, data)["word/document.xml"])
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range d.paras {
		out = append(out, string(p.text))
	}
	return out
}

func doEdit(t *testing.T, data []byte, op Op) ([]byte, string) {
	t.Helper()
	out, desc, err := Edit(data, op)
	if err != nil {
		t.Fatalf("%+v: %v", op, err)
	}
	return out, desc
}

func fail(t *testing.T, data []byte, op Op, want string) {
	t.Helper()
	if _, _, err := Edit(data, op); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%+v: got %v, want error containing %q", op, err, want)
	}
}

func has(ts []string, s string) bool {
	for _, x := range ts {
		if x == s {
			return true
		}
	}
	return false
}

func index(ts []string, s string) int {
	for i, x := range ts {
		if x == s {
			return i
		}
	}
	return -1
}

// checkOnlyChanged asserts every byte of document.xml outside [start,end) of the
// original is preserved (the new bytes have the same prefix and suffix).
func checkOutside(t *testing.T, before, after []byte, start, end int) {
	t.Helper()
	b, a := entries(t, before)["word/document.xml"], entries(t, after)["word/document.xml"]
	if !bytes.Equal(b[:start], a[:start]) || !bytes.HasSuffix(a, b[end:]) {
		t.Fatalf("bytes outside the edit changed")
	}
}

func paraRange(t *testing.T, data []byte, text string) (int, int) {
	t.Helper()
	d, _ := parse(entries(t, data)["word/document.xml"])
	for _, p := range d.paras {
		if strings.HasPrefix(string(p.text), text) {
			return p.start, p.end
		}
	}
	t.Fatalf("no paragraph %q", text)
	return 0, 0
}

func TestReplaceWithinOneRun(t *testing.T) {
	in := fixture(t)
	out, desc := doEdit(t, in, Op{Kind: Replace, Old: "Notice period: 30 days.", New: "Notice period: 60 days."})
	if !has(texts(t, out), "Notice period: 60 days.") || !strings.Contains(desc, "replaced 1") {
		t.Fatalf("%v %s", texts(t, out), desc)
	}
	s, e := paraRange(t, in, "Notice period")
	checkOutside(t, in, out, s, e)
}

func TestReplaceAcrossRuns(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Replace, Old: "due in 30 days", New: "due in 60 days"})
	if !has(texts(t, out), "Payment is due in 60 days of the invoice date & no later.") {
		t.Fatalf("%v", texts(t, out))
	}
	x := string(entries(t, out)["word/document.xml"])
	// the bold run holding "30" is gone, the new text sits in the first run (not bold)
	if strings.Contains(x, ">30</w:t>") || strings.Contains(x, `w:name="pay"`) == false {
		t.Fatalf("unexpected runs/bookmark handling:\n%s", x)
	}
	if !strings.Contains(x, `<w:t xml:space="preserve">Payment is due in 60 days</w:t>`) {
		t.Fatalf("new text not in the first run:\n%s", x)
	}
	s, e := paraRange(t, in, "Payment is due")
	checkOutside(t, in, out, s, e)
}

func TestReplaceAll(t *testing.T) {
	in := fixture(t)
	fail(t, in, Op{Kind: Replace, Old: "30 days", New: "60 days"}, "occurs 2 times")
	out, desc := doEdit(t, in, Op{Kind: Replace, Old: "30 days", New: "60 days", ReplaceAll: true})
	ts := texts(t, out)
	if !has(ts, "Notice period: 60 days.") || !has(ts, "Payment is due in 60 days of the invoice date & no later.") || !strings.Contains(desc, "2 occurrence") {
		t.Fatalf("%v", ts)
	}
}

func TestDeleteParagraph(t *testing.T) {
	in := fixture(t)
	out, desc := doEdit(t, in, Op{Kind: Replace, Old: "Second item", New: ""})
	if has(texts(t, out), "Second item") || len(texts(t, out)) != len(texts(t, in))-1 || !strings.Contains(desc, "removing 1") {
		t.Fatalf("%v %s", texts(t, out), desc)
	}
	// deleting text inside a paragraph keeps the paragraph
	out, _ = doEdit(t, in, Op{Kind: Replace, Old: "of the invoice date ", New: ""})
	if !has(texts(t, out), "Payment is due in 30 days  & no later.") {
		t.Fatalf("%v", texts(t, out))
	}
}

func TestInsertAfterBefore(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "Notice period", Text: "Late payment accrues interest."})
	ts := texts(t, out)
	i := index(ts, "Notice period: 30 days.")
	if ts[i+1] != "Late payment accrues interest." {
		t.Fatalf("%v", ts)
	}
	x := string(entries(t, out)["word/document.xml"])
	if !strings.Contains(x, `<w:p><w:pPr><w:pStyle w:val="Tijelo"/></w:pPr><w:r><w:t xml:space="preserve">Late payment accrues interest.</w:t></w:r></w:p>`) {
		t.Fatalf("new paragraph does not copy its neighbour:\n%s", x)
	}
	s, _ := paraRange(t, in, "Notice period")
	_, e := paraRange(t, in, "Notice period")
	checkOutside(t, in, out, e, e)
	_ = s
	out, _ = doEdit(t, in, Op{Kind: Insert, Anchor: "Notice period", Before: true, Text: "Before text."})
	ts = texts(t, out)
	if ts[index(ts, "Notice period: 30 days.")-1] != "Before text." {
		t.Fatalf("%v", ts)
	}
	// a passage from the middle of a paragraph, across runs
	out, _ = doEdit(t, in, Op{Kind: Insert, Anchor: "30 days of the invoice", Text: "Two\n\nparagraphs."})
	ts = texts(t, out)
	i = index(ts, "Payment is due in 30 days of the invoice date & no later.")
	if ts[i+1] != "Two" || ts[i+2] != "paragraphs." {
		t.Fatalf("%v", ts)
	}
}

func TestInsertNextToHeadingUsesBodyStyle(t *testing.T) {
	in := fixture(t)
	for _, c := range []struct {
		anchor string
		before bool
	}{{"Rental agreement", false}, {"# Rental agreement", false}} {
		out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: c.anchor, Before: c.before, Text: "Intro text."})
		ts := texts(t, out)
		if ts[1] != "Intro text." {
			t.Fatalf("%v", ts)
		}
		x := string(entries(t, out)["word/document.xml"])
		i := strings.Index(x, "Intro text.")
		if p := x[strings.LastIndex(x[:i], "<w:p>"):i]; strings.Contains(p, "Naslov1") || !strings.Contains(p, "Tijelo") {
			t.Fatalf("new paragraph copied the heading's style: %s", p)
		}
	}
	// Before the first heading nothing precedes it: no properties.
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "Rental agreement", Before: true, Text: "Cover."})
	x := string(entries(t, out)["word/document.xml"])
	if !strings.Contains(x, `<w:body><w:p><w:r><w:t xml:space="preserve">Cover.</w:t></w:r></w:p>`) && !strings.Contains(x, `<w:body><w:p><w:pPr><w:pStyle w:val="Tijelo"/>`) {
		t.Fatalf("%s", x[:600])
	}
}

func TestInsertHeadingByNameNotID(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "Notice period", Text: "## Subsection"})
	x := string(entries(t, out)["word/document.xml"])
	if !strings.Contains(x, `<w:pStyle w:val="Naslov2"/>`) || !has(texts(t, out), "Subsection") {
		t.Fatalf("%s", x)
	}
	// No heading style in the document: a bold paragraph.
	plainDoc := docxWith(t, `<w:p><w:r><w:t>Only text</w:t></w:r></w:p>`, "")
	out, _ = doEdit(t, plainDoc, Op{Kind: Insert, Anchor: "Only text", Text: "# Title"})
	x = string(entries(t, out)["word/document.xml"])
	if !strings.Contains(x, `<w:rPr><w:b/></w:rPr><w:t xml:space="preserve">Title</w:t>`) {
		t.Fatalf("%s", x)
	}
}

func TestTableRows(t *testing.T) {
	in := fixture(t)
	out, desc := doEdit(t, in, Op{Kind: Insert, Anchor: "| Rent | 500 EUR |", Text: "| Deposit | 1000 EUR |"})
	ts := texts(t, out)
	i := index(ts, "500 EUR")
	if ts[i+1] != "Deposit" || ts[i+2] != "1000 EUR" || !strings.Contains(desc, "table row") {
		t.Fatalf("%v %s", ts, desc)
	}
	x := string(entries(t, out)["word/document.xml"])
	if strings.Count(x, "<w:tr>") != 3 || strings.Contains(x[strings.Index(x, "Deposit")-400:], "tblHeader") {
		t.Fatalf("row not cloned cleanly:\n%s", x)
	}
	// a cell's text finds the row too, and before works; the header flag is not cloned
	out, _ = doEdit(t, in, Op{Kind: Insert, Anchor: "Item", Before: true, Text: "| Zero | 0 |"})
	if ts = texts(t, out); ts[index(ts, "Item")-2] != "Zero" {
		t.Fatalf("%v", ts)
	}
	fail(t, in, Op{Kind: Insert, Anchor: "500 EUR", Text: "| a | b | c |"}, "has 3 cells but the row it goes next to has 2")
	fail(t, in, Op{Kind: Insert, Anchor: "Notice period", Text: "| a | b |"}, "not in a table")
	fail(t, in, Op{Kind: Insert, Anchor: "500 EUR", Text: "| a | b |\n|---|---|\n| 1 | 2 |"}, "new tables")
	fail(t, in, Op{Kind: Insert, Anchor: "| Rent | 500 EUR |", Text: "plain"}, "names a table row")
	fail(t, in, Op{Kind: Insert, Anchor: "| Nope | 1 |", Text: "| a | b |"}, "no table row")
}

func TestInsertTextInsideCell(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "500 EUR", Text: "per month"})
	x := string(entries(t, out)["word/document.xml"])
	if !strings.Contains(x, `<w:t>500 EUR</w:t></w:r></w:p><w:p><w:r><w:t xml:space="preserve">per month</w:t></w:r></w:p></w:tc>`) {
		t.Fatalf("paragraph not inside the same cell:\n%s", x)
	}
}

func TestAppend(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Append, Text: "Closing paragraph.\n\nSecond **closing** one."})
	ts := texts(t, out)
	if ts[len(ts)-2] != "Closing paragraph." || ts[len(ts)-1] != "Second closing one." {
		t.Fatalf("%v", ts)
	}
	x := string(entries(t, out)["word/document.xml"])
	if i := strings.Index(x, "Closing paragraph."); !strings.Contains(x[i:], "</w:p><w:sectPr>") || !strings.Contains(x[i-120:i], `w:val="Tijelo"`) {
		t.Fatalf("append placement/style:\n%s", x[i-200:])
	}
	if !strings.Contains(x, `<w:rPr><w:b/></w:rPr><w:t xml:space="preserve">closing</w:t>`) {
		t.Fatalf("bold span missing:\n%s", x)
	}
	_, e := paraRange(t, in, "Cijena je")
	checkOutside(t, in, out, e, e)
}

func TestListItems(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "Second item", Text: "- Third item"})
	x := string(entries(t, out)["word/document.xml"])
	if !has(texts(t, out), "Third item") || strings.Count(x, `<w:numId w:val="3"/>`) != 3 {
		t.Fatalf("item did not join the list: %v", texts(t, out))
	}
	out, _ = doEdit(t, in, Op{Kind: Insert, Anchor: "Notice period", Text: "- not a list here"})
	if !has(texts(t, out), "- not a list here") {
		t.Fatalf("%v", texts(t, out))
	}
	// the marker as read_doc shows it still anchors
	doEdit(t, in, Op{Kind: Insert, Anchor: "- First item", Text: "- 1b"})
	doEdit(t, in, Op{Kind: Replace, Old: "1. First item", New: "Opening item"})
}

func TestMarkdownQueriesMatch(t *testing.T) {
	in := fixture(t)
	for _, old := range []string{"**Notice** period:   30\ndays.", "## Rental agreement", "_Notice_ period"} {
		if _, _, err := Edit(in, Op{Kind: Insert, Anchor: old, Text: "x"}); err != nil {
			t.Errorf("%q: %v", old, err)
		}
	}
}

func TestSpecialCharacters(t *testing.T) {
	in := fixture(t)
	txt := `Tom & "Jerry" <b>tag</b> 'q' Čćžšđ — €`
	out, _ := doEdit(t, in, Op{Kind: Insert, Anchor: "Notice period", Text: txt})
	if !has(texts(t, out), txt) {
		t.Fatalf("%v", texts(t, out))
	}
	out, _ = doEdit(t, in, Op{Kind: Replace, Old: "1.500 €", New: `2.000 & <ž>`})
	if !has(texts(t, out), `Cijena je 2.000 & <ž> — plaća se u roku.`) {
		t.Fatalf("%v", texts(t, out))
	}
	if !strings.Contains(string(entries(t, out)["word/document.xml"]), "2.000 &amp; &lt;ž&gt;") {
		t.Fatal("not escaped")
	}
}

func TestRefusals(t *testing.T) {
	in := fixture(t)
	fail(t, in, Op{Kind: Replace, Old: "the example link", New: "x"}, "hyperlink")
	fail(t, in, Op{Kind: Replace, Old: "Page 3 of", New: "x"}, "field")
	fail(t, in, Op{Kind: Replace, Old: "Boxed words", New: "x"}, "text box")
	fail(t, in, Op{Kind: Insert, Anchor: "Boxed words", Text: "x"}, "text box")
	fail(t, in, Op{Kind: Replace, Old: "nowhere", New: "x"}, "was not found")
	fail(t, in, Op{Kind: Insert, Anchor: "nowhere", Text: "x"}, "was not found")
	fail(t, in, Op{Kind: Insert, Anchor: "days", Text: "x"}, "occurs 2 times")
	fail(t, in, Op{Kind: Replace, Old: "500 EUR", New: "a\nb"}, "single line")
	fail(t, in, Op{Kind: Replace, Old: "500 EUR", New: "500 EUR"}, "identical")
	fail(t, in, Op{Kind: Replace, Old: "Item | Price", New: "a"}, "was not found")
	fail(t, in, Op{Kind: Append, Text: "| a | b |"}, "cannot be appended")
	fail(t, []byte("not a zip"), Op{Kind: Append, Text: "x"}, "not a readable .docx")
	tracked := docxWith(t, `<w:p><w:ins w:id="1" w:author="A"><w:r><w:t>new words</w:t></w:r></w:ins></w:p>`, "")
	fail(t, tracked, Op{Kind: Append, Text: "x"}, "tracked changes")
	var nodoc bytes.Buffer
	zw := zip.NewWriter(&nodoc)
	zw.Create("other.xml")
	zw.Close()
	fail(t, nodoc.Bytes(), Op{Kind: Append, Text: "x"}, "no word/document.xml")
}

func TestOtherEntriesAreIdentical(t *testing.T) {
	in := fixture(t)
	out, _ := doEdit(t, in, Op{Kind: Replace, Old: "500 EUR", New: "600 EUR"})
	a, b := entries(t, in), entries(t, out)
	if len(a) != len(b) {
		t.Fatal("entry count changed")
	}
	for name, data := range a {
		if name != "word/document.xml" && !bytes.Equal(data, b[name]) {
			t.Errorf("%s changed", name)
		}
	}
	zr, _ := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if zr.File[0].Name != "[Content_Types].xml" {
		t.Errorf("entry order changed: %s first", zr.File[0].Name)
	}
}

func TestEveryEditKeepsOtherParagraphs(t *testing.T) {
	in := fixture(t)
	before := texts(t, in)
	out, _ := doEdit(t, in, Op{Kind: Replace, Old: "500 EUR", New: "600 EUR"})
	after := texts(t, out)
	for i := range before {
		want := before[i]
		if want == "500 EUR" {
			want = "600 EUR"
		}
		if after[i] != want {
			t.Errorf("paragraph %d: %q vs %q", i, after[i], want)
		}
	}
}

func TestVerifyRejectsBrokenOutput(t *testing.T) {
	good := entries(t, fixture(t))["word/document.xml"]
	d, _ := parse(good)
	var exp []string
	for _, p := range d.paras {
		exp = append(exp, string(p.text))
	}
	if err := verify(good, exp); err != nil {
		t.Fatal(err)
	}
	if err := verify(bytes.Replace(good, []byte("Rent<"), []byte("Rant<"), 1), exp); err == nil {
		t.Error("a changed paragraph was accepted")
	}
	if err := verify(bytes.Replace(good, []byte("</w:body>"), nil, 1), exp); err == nil {
		t.Error("malformed XML was accepted")
	}
	if err := verify(good, exp[1:]); err == nil {
		t.Error("a missing paragraph was accepted")
	}
	swapped := append([]string{exp[1], exp[0]}, exp[2:]...)
	if err := verify(good, swapped); err == nil {
		t.Error("reordered paragraphs were accepted")
	}
}
