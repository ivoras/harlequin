package mdconv

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func mkZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

func TestConvert(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want []string
	}{
		{"a.html", []byte("<h1>Title</h1><p>Hello <b>world</b></p>"), []string{"# Title", "**world**"}},
		{"a.pptx", mkZip(t, map[string]string{
			"ppt/slides/slide2.xml":  `<p:sld xmlns:a="x" xmlns:p="y"><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`,
			"ppt/slides/slide10.xml": `<p:sld xmlns:a="x" xmlns:p="y"><a:p><a:r><a:t>Tenth</a:t></a:r></a:p></p:sld>`,
		}), []string{"## Slide 2\n\nSecond", "## Slide 10\n\nTenth"}},
		{"a.xlsx", mkZip(t, map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
			"xl/sharedStrings.xml":       `<sst><si><t>Name</t></si><si><t>Qty</t></si><si><t>Pipe|x</t></si></sst>`,
			"xl/worksheets/sheet1.xml": `<worksheet><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>
<row r="2"><c r="A2" t="s"><v>2</v></c><c r="C2"><v>7</v></c></row></sheetData></worksheet>`,
		}), []string{"## Data", "| Name |  | Qty |", `| Pipe\|x |  | 7 |`}},
		{"a.odt", mkZip(t, map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t" xmlns:table="b"><office:body>
<text:h text:outline-level="2">Head</text:h><text:p>Para<text:s/>one</text:p>
<text:list><text:list-item><text:p>item</text:p></text:list-item></text:list>
<table:table><table:table-row><table:table-cell><text:p>A</text:p></table:table-cell><table:table-cell><text:p>B</text:p></table:table-cell></table:table-row></table:table>
</office:body></office:document-content>`}), []string{"## Head", "Para one", "- item", "| A | B |"}},
		{"a.epub", mkZip(t, map[string]string{
			"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/c.opf"/></rootfiles></container>`,
			"OEBPS/c.opf":            `<package><manifest><item id="a" href="ch%201.xhtml"/><item id="b" href="ch2.xhtml"/></manifest><spine><itemref idref="b"/><itemref idref="a"/></spine></package>`,
			"OEBPS/ch 1.xhtml":       `<html><body><p>First</p></body></html>`,
			"OEBPS/ch2.xhtml":        `<html><body><h1>Zero</h1></body></html>`,
		}), []string{"# Zero\n\nFirst"}},
		{"a.rtf", []byte(`{\rtf1\ansi{\fonttbl{\f0 Arial;}}{\*\generator X;}\f0 Hello \b bold\b0  caf\'e9\par Next \u8364?\par}`), []string{"Hello bold café\n\nNext €"}},
	}
	for _, c := range cases {
		got, err := Convert(c.name, c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in:\n%s", c.name, w, got)
			}
		}
	}
	// Multi-sheet: index, empty + hidden sheets kept, absolute rel target, long sheet blocked.
	var long strings.Builder
	long.WriteString(`<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c></row>`)
	for r := 2; r <= 90; r++ {
		fmt.Fprintf(&long, `<row r="%d"><c r="A%d"><v>%d</v></c></row>`, r, r, r)
	}
	long.WriteString(`</sheetData></worksheet>`)
	multi := mkZip(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Big" sheetId="1" r:id="rId1"/><sheet name="Blank" sheetId="2" r:id="rId2"/><sheet name="Old" state="hidden" sheetId="3" r:id="rId3"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="/xl/worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Target="worksheets/sheet3.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Num</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   long.String(),
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData/></worksheet>`,
		"xl/worksheets/sheet3.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>x</v></c></row></sheetData></worksheet>`,
	})
	got, err := Convert("m.xlsx", multi)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"Workbook with 3 sheets:", "1. Big (89 rows × 1 columns)", "2. Blank (empty)", "3. Old (0 rows × 1 columns, hidden)",
		"## Sheet 1 of 3: Big", "## Sheet 2 of 3: Blank\n\n_(empty)_", "## Sheet 3 of 3: Old",
		"### Big (continued, rows 41–80)", "### Big (continued, rows 81–89)",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("multi-sheet: missing %q in:\n%s", w, got)
		}
	}
	if c := strings.Count(got, "| Num |"); c != 3 {
		t.Errorf("header row should repeat per block, got %d", c)
	}
	if _, err := Convert("a.odt", []byte("junk")); err == nil {
		t.Error("junk ODT should fail")
	}
	if _, err := Convert("a.txt", nil); err != ErrUnsupported {
		t.Error("txt should be unsupported")
	}
}
