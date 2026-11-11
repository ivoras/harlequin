package xlsxedit

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/ivoras/harlequin/internal/server/mdxlsx"
)

type styles struct{ title, header, money, total int }

// payroll builds an invented two-sheet workbook with styles, formulas that point
// at rows which will shift, a merged title, a defined name, validation,
// conditional formatting and a frozen pane.
func payroll(t testing.TB, withChart bool) ([]byte, styles) {
	t.Helper()
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "October")
	f.NewSheet("Summary")
	var s styles
	s.title, _ = f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	s.header, _ = f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"DDDDDD"}}})
	s.money, _ = f.NewStyle(&excelize.Style{NumFmt: 4, Font: &excelize.Font{Color: "1F4E79"}})
	s.total, _ = f.NewStyle(&excelize.Style{NumFmt: 4, Font: &excelize.Font{Bold: true}})
	f.SetCellValue("October", "A1", "Payroll October")
	f.MergeCell("October", "A1", "C1")
	f.SetCellStyle("October", "A1", "A1", s.title)
	f.SetSheetRow("October", "A2", &[]any{"Name", "Gross", "Net"})
	f.SetCellStyle("October", "A2", "C2", s.header)
	f.SetSheetRow("October", "A3", &[]any{"Ana Horvat", 1620})
	f.SetSheetRow("October", "A4", &[]any{"Ivan Kovač", 1500})
	f.SetCellFormula("October", "C3", "B3*0.73")
	f.SetCellFormula("October", "C4", "B4*0.73")
	f.SetCellValue("October", "A5", "Total")
	f.SetCellFormula("October", "B5", "SUM(B3:B4)")
	f.SetCellFormula("October", "C5", "SUM(C3:C4)")
	f.SetCellStyle("October", "B3", "C4", s.money)
	f.SetCellStyle("October", "A5", "C5", s.total)
	f.SetColWidth("October", "A", "A", 24)
	dv := excelize.NewDataValidation(true)
	dv.Sqref = "D3:D4"
	dv.SetDropList([]string{"paid", "open"})
	f.AddDataValidation("October", dv)
	dxf, _ := f.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetConditionalFormat("October", "B3:B4", []excelize.ConditionalFormatOptions{{Type: "cell", Criteria: ">", Value: "1600", Format: &dxf}})
	f.SetPanes("October", &excelize.Panes{Freeze: true, YSplit: 2, TopLeftCell: "A3", ActivePane: "bottomLeft"})
	f.SetDefinedName(&excelize.DefinedName{Name: "TotalGross", RefersTo: "October!$B$5"})
	f.SetCellValue("Summary", "A1", "Total gross")
	f.SetCellFormula("Summary", "B1", "October!B5")
	f.SetCellValue("Summary", "A2", "Ana")
	f.SetCellFormula("Summary", "B2", "October!B3")
	if withChart {
		f.AddChart("Summary", "E2", &excelize.Chart{Type: excelize.Col, Series: []excelize.ChartSeries{{Name: "Summary!$A$2", Categories: "October!$A$3:$A$4", Values: "October!$B$3:$B$4"}}})
	}
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), s
}

func reopen(t testing.TB, data []byte) *excelize.File {
	t.Helper()
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func edit(t *testing.T, data []byte, op Op) ([]byte, string) {
	t.Helper()
	out, desc, err := Edit(data, op)
	if err != nil {
		t.Fatalf("%+v: %v", op, err)
	}
	return out, desc
}

func fails(t *testing.T, data []byte, op Op, want string) {
	t.Helper()
	if out, _, err := Edit(data, op); err == nil || out != nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%+v: got %v, want error containing %q", op, err, want)
	}
}

func val(t testing.TB, f *excelize.File, sheet, addr string) string {
	t.Helper()
	v, _ := f.GetCellValue(sheet, addr, raw)
	return v
}

func formula(f *excelize.File, sheet, addr string) string {
	fo, _ := f.GetCellFormula(sheet, addr)
	return fo
}

func style(f *excelize.File, sheet, addr string) int {
	s, _ := f.GetCellStyle(sheet, addr)
	return s
}

func TestSetCells(t *testing.T) {
	in, s := payroll(t, false)
	out, desc := edit(t, in, Op{Kind: SetCells, Cells: map[string]any{
		"October!B3": 1700.0, "October!C3": "=B3*0.8", "October!D3": "paid", "Summary!B2": "=October!B3+1", "October!A4": nil,
	}})
	if !strings.Contains(desc, "set 5 cell(s) on sheet October, Summary") {
		t.Fatal(desc)
	}
	f := reopen(t, out)
	if val(t, f, "October", "B3") != "1700" || formula(f, "October", "C3") != "B3*0.8" || val(t, f, "October", "D3") != "paid" || formula(f, "Summary", "B2") != "October!B3+1" || val(t, f, "October", "A4") != "" {
		t.Fatal("values not set")
	}
	if val(t, f, "October", "C3") != "" {
		t.Fatalf("stale cached result kept: %q", val(t, f, "October", "C3"))
	}
	if style(f, "October", "B3") != s.money || style(f, "October", "C3") != s.money {
		t.Fatal("cell styles changed")
	}
	// Formulas elsewhere keep their text; Excel is told to recalculate on load.
	if formula(f, "October", "C4") != "B4*0.73" || formula(f, "October", "B5") != "SUM(B3:B4)" {
		t.Fatal("other formulas changed")
	}
	if cp, _ := f.GetCalcProps(); cp.FullCalcOnLoad == nil || !*cp.FullCalcOnLoad {
		t.Fatal("full calculation on load not requested")
	}
	if mc, _ := f.GetMergeCells("October"); len(mc) != 1 {
		t.Fatal("merge lost")
	}
}

func TestSetCellsDefaultSheetAndErrors(t *testing.T) {
	in, _ := payroll(t, false)
	fails(t, in, Op{Kind: SetCells, Cells: map[string]any{"B3": 1}}, `has no sheet`)
	fails(t, in, Op{Kind: SetCells, Cells: map[string]any{"B3": 1}}, `"October", "Summary"`)
	out, _ := edit(t, in, Op{Kind: SetCells, Sheet: "october", Cells: map[string]any{"B3": 1}})
	if val(t, reopen(t, out), "October", "B3") != "1" {
		t.Fatal("sheet argument ignored")
	}
	fails(t, in, Op{Kind: SetCells, Cells: map[string]any{"Nope!A1": 1}}, `there is no sheet "Nope"`)
	fails(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"ZZZZ99999": 1}}, "not a cell address")
	fails(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"B2:C3": 1}}, "not a cell address")
	fails(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"B1": 1}}, "merged range A1:C1")
	fails(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"B3": []any{1}}}, "unsupported value")
	fails(t, in, Op{Kind: SetCells, Sheet: "October"}, "cells is empty")
	// the top-left cell of a merge is writable
	edit(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"A1": "Payroll Oct"}})
	// a single-sheet workbook needs no sheet name
	md, _, _ := mdxlsx.Convert("# Only\n| a | b |\n|---|---|\n| 1 | x |\n")
	out, _ = edit(t, md, Op{Kind: SetCells, Cells: map[string]any{"B2": "y", "A3": "007"}})
	f := reopen(t, out)
	if val(t, f, "Only", "B2") != "y" || val(t, f, "Only", "A3") != "007" {
		t.Fatal("mdxlsx workbook edit failed")
	}
	if ty, _ := f.GetCellType("Only", "A3"); ty == excelize.CellTypeNumber {
		t.Fatal("text typed as a number")
	}
}

func TestAppendRows(t *testing.T) {
	in, s := payroll(t, false)
	out, desc := edit(t, in, Op{Kind: AppendRows, Sheet: "October", Text: "| Name | Gross |\n|---|---|\n| Marta Babić | 1800 | =B6*0.73 |\n| Luka Perić | 007 | |"})
	_ = desc
	f := reopen(t, out)
	if val(t, f, "October", "A6") != "Name" || val(t, f, "October", "A7") != "Marta Babić" || val(t, f, "October", "B7") != "1800" || formula(f, "October", "C7") != "B6*0.73" || val(t, f, "October", "B8") != "007" {
		t.Fatalf("rows wrong: %s", desc)
	}
	if style(f, "October", "B6") != s.total || style(f, "October", "C7") != s.total {
		t.Fatal("style of the row above not copied")
	}
	fails(t, in, Op{Kind: AppendRows, Sheet: "October", Text: "A free paragraph."}, "only Markdown table rows")
	fails(t, in, Op{Kind: AppendRows, Text: "| a |"}, "several sheets")
	fails(t, in, Op{Kind: AppendRows, Sheet: "October", Text: "|---|"}, "no table rows")
}

func TestInsertRowsShiftsEverything(t *testing.T) {
	in, s := payroll(t, false)
	out, desc := edit(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "Ana Horvat", Before: true, Text: "| Eva Novak | 1400 | =B3*0.73 |"})
	f := reopen(t, out)
	if val(t, f, "October", "A3") != "Eva Novak" || val(t, f, "October", "A4") != "Ana Horvat" || val(t, f, "October", "A6") != "Total" {
		t.Fatalf("rows not moved: %s", desc)
	}
	// formulas, defined names, merged ranges, validation and other sheets follow the shift
	if formula(f, "October", "B6") != "SUM(B4:B5)" || formula(f, "October", "C4") != "B4*0.73" || formula(f, "Summary", "B1") != "October!B6" || formula(f, "Summary", "B2") != "October!B4" {
		t.Fatalf("formulas not adjusted: %q %q %q %q", formula(f, "October", "B6"), formula(f, "October", "C4"), formula(f, "Summary", "B1"), formula(f, "Summary", "B2"))
	}
	if dn := f.GetDefinedName(); len(dn) != 1 || dn[0].RefersTo != "October!$B$6" {
		t.Fatalf("defined name: %+v", dn)
	}
	if mc, _ := f.GetMergeCells("October"); len(mc) != 1 || mc[0].GetStartAxis() != "A1" {
		t.Fatalf("merge: %v", mc)
	}
	if dv, _ := f.GetDataValidations("October"); len(dv) != 1 || dv[0].Sqref != "D4:D5" {
		t.Fatalf("validation: %+v", dv)
	}
	if style(f, "October", "B3") != s.money || style(f, "October", "B4") != s.money || style(f, "October", "B6") != s.total {
		t.Fatal("styles wrong after insert")
	}
	// after a quoted row, "row N", and several rows
	out, _ = edit(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "row 4", Text: "| A | 1 |\n| B | 2 |"})
	f = reopen(t, out)
	if val(t, f, "October", "A5") != "A" || val(t, f, "October", "A6") != "B" || val(t, f, "October", "A7") != "Total" || formula(f, "October", "B7") != "SUM(B3:B4)" {
		t.Fatal("insert after row 4 wrong")
	}
	out, _ = edit(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "| Ivan Kovač | 1500 |", Before: true, Text: "| X | 1 |"})
	if val(t, reopen(t, out), "October", "A4") != "X" {
		t.Fatal("row quoted with pipes")
	}
	fails(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "nowhere", Text: "| a |"}, "was not found")
	fails(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "0.73", Text: "| a |"}, "occurs in")
	fails(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "row 99", Text: "| a |"}, "has rows 1-5")
	fails(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "row 3", Text: "free text"}, "only Markdown table rows")
}

func TestInsertRefusedWithCharts(t *testing.T) {
	in, _ := payroll(t, true)
	fails(t, in, Op{Kind: InsertRows, Sheet: "October", Anchor: "row 3", Text: "| a | 1 |"}, "charts")
	out, _ := edit(t, in, Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"B3": 5}})
	if n := countPart(t, out, "xl/charts/chart"); n != 1 {
		t.Fatalf("chart lost: %d", n)
	}
	out, _ = edit(t, in, Op{Kind: AppendRows, Sheet: "October", Text: "| a | 1 |"})
	if countPart(t, out, "xl/charts/chart") != 1 {
		t.Fatal("chart lost on append")
	}
}

func countPart(t testing.TB, data []byte, prefix string) int {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, prefix) {
			n++
		}
	}
	return n
}

func TestReplace(t *testing.T) {
	in, s := payroll(t, false)
	out, desc := edit(t, in, Op{Kind: Replace, Old: "Ana Horvat", New: "Ana H."})
	f := reopen(t, out)
	if val(t, f, "October", "A3") != "Ana H." || !strings.Contains(desc, "1 cell") {
		t.Fatal(desc)
	}
	out, _ = edit(t, in, Op{Kind: Replace, Old: "0.73", New: "0.75", ReplaceAll: true, Sheet: "October"})
	f = reopen(t, out)
	if formula(f, "October", "C3") != "B3*0.75" || formula(f, "October", "C4") != "B4*0.75" || style(f, "October", "C3") != s.money {
		t.Fatal("formula replace")
	}
	out, _ = edit(t, in, Op{Kind: Replace, Old: "=B3*0.73", New: "=B3*0.5"})
	if formula(reopen(t, out), "October", "C3") != "B3*0.5" {
		t.Fatal("formula replace quoted with =")
	}
	out, _ = edit(t, in, Op{Kind: Replace, Old: "Total", New: "", Sheet: "October"})
	if val(t, reopen(t, out), "October", "A5") != "" {
		t.Fatal("empty replacement should clear")
	}
	fails(t, in, Op{Kind: Replace, Old: "0.73", New: "0.75"}, "occurs 2 times")
	fails(t, in, Op{Kind: Replace, Old: "nonexistent", New: "x"}, "was not found")
	fails(t, in, Op{Kind: Replace, Old: "1620", New: "1700"}, "use cells")
	fails(t, in, Op{Kind: Replace, Old: "=B3*0.73", New: "B3"}, "remove the =")
	fails(t, in, Op{Kind: Replace, Old: "Ana", New: "Ana", Sheet: "Summary"}, "identical")
	fails(t, in, Op{Kind: Replace, Old: "Ana", New: "Anja", Sheet: "Nope"}, "no sheet")
	// restricted to a sheet
	out, _ = edit(t, in, Op{Kind: Replace, Old: "Ana", New: "Anja", Sheet: "Summary"})
	if val(t, reopen(t, out), "Summary", "A2") != "Anja" || val(t, reopen(t, out), "October", "A3") != "Ana Horvat" {
		t.Fatal("sheet restriction")
	}
}

func TestReplaceRefusesMixedFormatting(t *testing.T) {
	f := excelize.NewFile()
	f.SetCellRichText("Sheet1", "A1", []excelize.RichTextRun{{Text: "Bold", Font: &excelize.Font{Bold: true}}, {Text: " plain"}})
	b, _ := f.WriteToBuffer()
	fails(t, b.Bytes(), Op{Kind: Replace, Old: "plain", New: "x"}, "mixed formatting")
}

func TestRefusedFiles(t *testing.T) {
	in, _ := payroll(t, false)
	op := Op{Kind: SetCells, Sheet: "October", Cells: map[string]any{"B3": 1}}
	fails(t, []byte("not a zip"), op, "not a readable .xlsx")
	fails(t, append(append([]byte{}, oleMagic...), make([]byte, 64)...), op, "old .xls")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.Create("other.txt")
	zw.Close()
	fails(t, buf.Bytes(), op, "no xl/workbook.xml")
	withVBA := addPart(t, in, "xl/vbaProject.bin", "BIN")
	fails(t, withVBA, op, "macros")
	if _, err := Grid([]byte("nope")); err == nil {
		t.Fatal("Grid accepted garbage")
	}
}

func addPart(t testing.TB, data []byte, name, content string) []byte {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		w, _ := zw.Create(f.Name)
		w.Write(b)
	}
	w, _ := zw.Create(name)
	w.Write([]byte(content))
	zw.Close()
	return buf.Bytes()
}

func entry(data []byte, name string) string {
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			return string(b)
		}
	}
	return ""
}

func replaceIn(t testing.TB, data []byte, name string, mod func(string) string) []byte {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == name {
			b = []byte(mod(string(b)))
		}
		w, _ := zw.Create(f.Name)
		w.Write(b)
	}
	zw.Close()
	return buf.Bytes()
}

// What survives an excelize round trip, measured: build a workbook with each
// feature (some injected as raw XML, the way Excel writes them), edit one cell,
// and check every feature is still there.
func TestFeaturesSurviveAnEdit(t *testing.T) {
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "Data")
	f.NewSheet("Other")
	f.SetSheetRow("Data", "A1", &[]any{"x", "y"})
	for i := 2; i <= 6; i++ {
		f.SetSheetRow("Data", fmt.Sprintf("A%d", i), &[]any{i, i * 2})
	}
	f.SetCellValue("Other", "A1", "untouched")
	f.SetCellRichText("Data", "D1", []excelize.RichTextRun{{Text: "Bold", Font: &excelize.Font{Bold: true}}, {Text: " plain"}})
	f.SetDefinedName(&excelize.DefinedName{Name: "Range1", RefersTo: "Data!$A$2:$A$6"})
	dv := excelize.NewDataValidation(true)
	dv.Sqref = "E2:E5"
	dv.SetDropList([]string{"a", "b"})
	f.AddDataValidation("Data", dv)
	dxf, _ := f.NewConditionalStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetConditionalFormat("Data", "B2:B6", []excelize.ConditionalFormatOptions{{Type: "cell", Criteria: ">", Value: "5", Format: &dxf}})
	f.SetPanes("Data", &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	f.AddChart("Data", "H2", &excelize.Chart{Type: excelize.Col, Series: []excelize.ChartSeries{{Values: "Data!$B$2:$B$6"}}})
	var pb bytes.Buffer
	png.Encode(&pb, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	f.AddPictureFromBytes("Data", "H20", &excelize.Picture{Extension: ".png", File: pb.Bytes()})
	f.AddComment("Data", excelize.Comment{Cell: "A1", Author: "a", Paragraph: []excelize.RichTextRun{{Text: "note"}}})
	f.SetCellHyperLink("Data", "A2", "https://example.com", "External")
	f.AddTable("Other", &excelize.Table{Range: "A1:A2", Name: "T1"})
	f.SetColWidth("Data", "A", "A", 31)
	f.SetCellValue("Data", "J1", 1)
	base, _ := f.WriteToBuffer()
	b := base.Bytes()
	// raw XML only Excel itself writes: a sparkline group, a shape in a drawing, a dynamic-array cell
	spark := `<extLst><ext uri="{05C60535-1F16-4fd7-B633-0D5D4F9F6A57}" xmlns:x14="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main" xmlns:xm="http://schemas.microsoft.com/office/excel/2006/main"><x14:sparklineGroups><x14:sparklineGroup><x14:sparklines><x14:sparkline><xm:f>Data!A2:B2</xm:f><xm:sqref>K1</xm:sqref></x14:sparkline></x14:sparklines></x14:sparklineGroup></x14:sparklineGroups></ext></extLst>`
	b = replaceIn(t, b, "xl/worksheets/sheet1.xml", func(s string) string {
		s = strings.Replace(s, "</worksheet>", spark+"</worksheet>", 1)
		return strings.Replace(s, `<c r="J1"`, `<c r="I1" cm="1"><f t="array" ref="I1:I2">A2:A3*2</f><v>4</v></c><c r="J1"`, 1)
	})
	shape := `<xdr:twoCellAnchor xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><xdr:from><xdr:col>9</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>29</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:from><xdr:to><xdr:col>12</xdr:col><xdr:colOff>0</xdr:colOff><xdr:row>32</xdr:row><xdr:rowOff>0</xdr:rowOff></xdr:to><xdr:sp macro="" textlink=""><xdr:nvSpPr><xdr:cNvPr id="9" name="TextBox 1"/><xdr:cNvSpPr txBox="1"/></xdr:nvSpPr><xdr:spPr/><xdr:txBody><a:bodyPr/><a:p><a:r><a:t>Shape words</a:t></a:r></a:p></xdr:txBody></xdr:sp><xdr:clientData/></xdr:twoCellAnchor>`
	b = replaceIn(t, b, "xl/drawings/drawing1.xml", func(s string) string { return strings.Replace(s, "</xdr:wsDr>", shape+"</xdr:wsDr>", 1) })
	b = addPart(t, b, "xl/slicers/slicer1.xml", "<slicers/>")
	b = addPart(t, b, "customXml/item1.xml", "<x/>")

	out, _ := edit(t, b, Op{Kind: SetCells, Sheet: "Data", Cells: map[string]any{"B3": 99}})
	g := reopen(t, out)
	check := func(name string, ok bool) {
		t.Helper()
		if !ok {
			t.Errorf("lost on edit: %s", name)
		}
	}
	check("edited cell", val(t, g, "Data", "B3") == "99")
	check("second sheet", val(t, g, "Other", "A1") == "untouched")
	check("chart", countPart(t, out, "xl/charts/chart") == 1)
	check("image", countPart(t, out, "xl/media/") == 1)
	check("shape in drawing", strings.Contains(entry(out, "xl/drawings/drawing1.xml"), "Shape words"))
	check("defined name", len(g.GetDefinedName()) == 1)
	dvs, _ := g.GetDataValidations("Data")
	check("data validation", len(dvs) == 1)
	check("conditional formatting", strings.Contains(entry(out, "xl/worksheets/sheet1.xml"), "<conditionalFormatting"))
	pn, _ := g.GetPanes("Data")
	check("frozen pane", pn.Freeze && pn.YSplit == 1)
	cm, _ := g.GetComments("Data")
	check("comment", len(cm) == 1)
	tb, _ := g.GetTables("Other")
	check("table", len(tb) == 1)
	hasLink, link, _ := g.GetCellHyperLink("Data", "A2")
	check("hyperlink", hasLink && link == "https://example.com")
	w, _ := g.GetColWidth("Data", "A")
	check("column width", w == 31)
	rt, _ := g.GetCellRichText("Data", "D1")
	check("rich text", len(rt) == 2 && rt[0].Font != nil && rt[0].Font.Bold)
	check("sparkline", strings.Contains(entry(out, "xl/worksheets/sheet1.xml"), "sparklineGroup"))
	check("dynamic array marker", strings.Contains(entry(out, "xl/worksheets/sheet1.xml"), `cm="1"`))
	check("slicer part", countPart(t, out, "xl/slicers/") == 1)
	check("custom xml", countPart(t, out, "customXml/") == 1)
}

func TestVerifyRejectsWrongOutput(t *testing.T) {
	in, _ := payroll(t, false)
	f := reopen(t, in)
	snapIn, _ := snapshot(f)
	// a different cell than the one the edit was meant to touch
	g := reopen(t, in)
	g.SetCellValue("October", "A3", "Tampered")
	bad, _ := g.WriteToBuffer()
	meant := []change{{sheet: "October", addr: "B3", to: cell{typ: "n", val: "5"}}}
	if err := verify(in, bad.Bytes(), snapIn, f.GetSheetList(), meant, insertion{}, nil); err == nil {
		t.Fatal("a wrong output was accepted")
	}
	// the intended change missing
	if err := verify(in, in, snapIn, f.GetSheetList(), meant, insertion{}, nil); err == nil {
		t.Fatal("a missing change was accepted")
	}
	// a formula replaced by its value
	h := reopen(t, in)
	h.SetCellFormula("October", "C3", "")
	h.SetCellValue("October", "C3", 1182.6)
	bad, _ = h.WriteToBuffer()
	if err := verify(in, bad.Bytes(), snapIn, f.GetSheetList(), nil, insertion{}, nil); err == nil {
		t.Fatal("a lost formula was accepted")
	}
	// a lost feature
	withChart, _ := payroll(t, true)
	fc := reopen(t, withChart)
	sc, _ := snapshot(fc)
	noChart, _ := payroll(t, false)
	if err := verify(withChart, noChart, sc, fc.GetSheetList(), nil, insertion{}, nil); err == nil || !strings.Contains(err.Error(), "charts") {
		t.Fatalf("a lost chart was accepted: %v", err)
	}
	// the unmodified output of an honest edit passes
	if err := verify(in, in, snapIn, f.GetSheetList(), nil, insertion{}, map[string][]string{"October": {"A1:C1"}}); err != nil {
		t.Fatal(err)
	}
}

func TestShiftFormula(t *testing.T) {
	ins := insertion{sheet: "S", at: 3, n: 2}
	for in, want := range map[string]string{
		"SUM(A1:A5)":            "SUM(A1:A7)",
		"A2+A3":                 "A2+A5",
		"$B$4*2":                "$B$6*2",
		`"A9"&A9`:               `"A9"&A11`,
		"Other!A9":              "Other!A9",
		"'S'!A9+LOG10(100)":     "'S'!A11+LOG10(100)",
		"IF(A3>0,\"a\"\"3\",1)": "IF(A5>0,\"a\"\"3\",1)",
	} {
		if got := shiftFormula(in, "S", ins); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if got := shiftFormula("S!A9", "Other", ins); got != "S!A11" {
		t.Errorf("qualified ref from another sheet: %s", got)
	}
	if got := shiftFormula("A9", "Other", ins); got != "A9" {
		t.Errorf("unqualified ref on another sheet: %s", got)
	}
}

func TestGrid(t *testing.T) {
	in, _ := payroll(t, false)
	g, err := Grid(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Sheet \"October\" (A1:C5)", "|   | A | B | C |", "|---|---|---|---|",
		"| 3 | Ana Horvat | 1620 | =B3*0.73 |", "| 5 | Total | =SUM(B3:B4) | =SUM(C3:C4) |", "Merged: A1:C1",
		"## Sheet \"Summary\" (A1:B2)", "| 1 | Total gross | =October!B5 |",
	} {
		if !strings.Contains(g, want) {
			t.Errorf("missing %q in:\n%s", want, g)
		}
	}
}
