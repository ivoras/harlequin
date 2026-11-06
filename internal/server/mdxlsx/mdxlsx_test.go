package mdxlsx

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const sample = `# Sheet: Q1 sales
| Region | Units | Price | Total |
|---|---:|---:|---|
| North | 120 | 9.5 | =B2*C2 |
| South \| East | 80 | 10.0 | =B3*C3 |

Notes are not part of a sheet.

| Code | Serial |
| - | - |
| 007 | 1234567890123456 |

## Summary
| Metric | Value |
| --- | --- |
| Share | 0.95 |
| Errors | #N/A |
| Docling pipe | a &#124; b |
`

func TestParse(t *testing.T) {
	sheets, st := Parse(sample)
	if st.Sheets != 2 || st.Tables != 3 || st.Ignored != 1 {
		t.Fatalf("stats %+v", st)
	}
	if sheets[0].Name != "Q1 sales" || sheets[1].Name != "Summary" {
		t.Fatalf("names %q %q", sheets[0].Name, sheets[1].Name)
	}
	if got := sheets[0].Tables[0][2]; !reflect.DeepEqual(got, []string{"South | East", "80", "10.0", "=B3*C3"}) {
		t.Fatalf("escaped pipe row: %q", got)
	}
	if got := sheets[1].Tables[0][3][1]; got != "a | b" {
		t.Fatalf("entity not decoded: %q", got)
	}
	if len(sheets[0].Tables[0]) != 3 { // separator skipped
		t.Fatalf("rows %d", len(sheets[0].Tables[0]))
	}
}

func TestCellTyping(t *testing.T) {
	for v, want := range map[string]bool{
		"120": true, "-2.5": true, "0.95": true, "2020.0": true, "1e6": true,
		"007": false, "1234567890123456": false, "1,000": false, "€5": false, "#N/A": false, "": false,
	} {
		if isNumber(v) != want {
			t.Errorf("isNumber(%q) = %v", v, !want)
		}
	}
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA"} {
		if colName(i) != want {
			t.Errorf("colName(%d) = %s", i, colName(i))
		}
	}
}

func TestSheetNames(t *testing.T) {
	got := sheetNames([]Sheet{{Name: "Q1/Q2: [draft]?"}, {Name: ""}, {Name: "q1-q2- (draft)"}, {Name: strings.Repeat("x", 40)}})
	want := []string{"Q1-Q2- (draft)", "Sheet2", "q1-q2- (draft) (2)", strings.Repeat("x", 31)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
}

// Every part of the package must be well-formed XML, and the cells typed right.
func TestWriteWellFormed(t *testing.T) {
	data, st, err := Convert(sample)
	if err != nil || st.Tables != 3 {
		t.Fatal(err, st)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		parts[f.Name] = string(b)
		d := xml.NewDecoder(bytes.NewReader(b))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		}
	}
	s1 := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<c r="B2"><v>120</v></c>`, `<c r="C3" s="2"><v>10.0</v></c>`, `<c r="D2"><f>B2*C2</f></c>`,
		`<c r="A1" s="1" t="inlineStr">`, `South | East`, `<c r="A6" t="inlineStr"><is><t xml:space="preserve">007</t>`} {
		if !strings.Contains(s1, want) {
			t.Errorf("sheet1 lacks %s", want)
		}
	}
	if !strings.Contains(parts["xl/styles.xml"], `formatCode="0.00"`) {
		t.Error("fixed-decimals number formats missing")
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="Q1 sales"`) {
		t.Error("sheet name missing")
	}
	if _, _, err := Convert("just text"); err == nil {
		t.Error("expected error without tables")
	}
}

// TestDoclingRoundTrip converts Markdown -> xlsx -> docling -> Markdown and
// checks the cell grids match. Needs a docling-serve: HARLEQUIN_DOCLING_URL
// (e.g. http://127.0.0.1:5001). MDXLSX_ROUNDTRIP_MD may name a real docling
// .md output (e.g. of a production workbook) to round-trip as well.
func TestDoclingRoundTrip(t *testing.T) {
	base := os.Getenv("HARLEQUIN_DOCLING_URL")
	if base == "" {
		t.Skip("HARLEQUIN_DOCLING_URL not set")
	}
	inputs := map[string]string{"synthetic": strings.ReplaceAll(sample, "=B2*C2", "1140")}
	inputs["synthetic"] = strings.ReplaceAll(inputs["synthetic"], "=B3*C3", "800")
	if p := os.Getenv("MDXLSX_ROUNDTRIP_MD"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		inputs[p] = string(b)
	}
	for name, md := range inputs {
		data, _, err := Convert(md)
		if err != nil {
			t.Fatal(name, err)
		}
		back := doclingMarkdown(t, base, data)
		want, got := grids(md), grids(back)
		if len(want) != len(got) {
			t.Fatalf("%s: %d tables in, %d back", name, len(want), len(got))
		}
		for i := range want {
			if !reflect.DeepEqual(want[i], got[i]) {
				in, out := firstDiff(want[i], got[i])
				t.Fatalf("%s: table %d differs\nin:   %q\nback: %q", name, i, in, out)
			}
		}
		t.Logf("%s: %d tables round-tripped", name, len(want))
	}
}

// grids flattens every table (any sheet) into comparable form: trailing empty
// cells trimmed (ragged rows), numbers compared by value (docling prints the
// stored value: 9.50 -> 9.5), and formulas as empty — the file carries no
// computed value until Excel calculates it.
func grids(md string) [][][]string {
	sheets, _ := Parse(md)
	var out [][][]string
	for _, s := range sheets {
		for _, tb := range s.Tables {
			var g [][]string
			for _, row := range tb {
				row = append([]string(nil), row...)
				for i, c := range row {
					if strings.HasPrefix(c, "=") {
						row[i] = ""
					} else if isNumber(c) {
						f, _ := strconv.ParseFloat(c, 64)
						row[i] = strconv.FormatFloat(f, 'g', -1, 64)
					}
				}
				for len(row) > 0 && row[len(row)-1] == "" {
					row = row[:len(row)-1]
				}
				g = append(g, row)
			}
			out = append(out, g)
		}
	}
	return out
}

func firstDiff(a, b [][]string) ([]string, []string) {
	for i := 0; i < len(a) && i < len(b); i++ {
		if !reflect.DeepEqual(a[i], b[i]) {
			return a[i], b[i]
		}
	}
	return []string{fmt.Sprint("rows: ", len(a))}, []string{fmt.Sprint("rows: ", len(b))}
}

func doclingMarkdown(t *testing.T, base string, xlsx []byte) string {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("files", "roundtrip.xlsx")
	fw.Write(xlsx)
	mw.WriteField("to_formats", "md")
	mw.WriteField("md_compact_tables", "true")
	mw.Close()
	c := &http.Client{Timeout: 3 * time.Minute}
	resp, err := c.Post(strings.TrimRight(base, "/")+"/v1/convert/file", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Status   string `json:"status"`
		Document struct {
			MD string `json:"md_content"`
		} `json:"document"`
		Errors any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Status != "success" {
		t.Fatalf("docling: status %s %q %v %v", resp.Status, out.Status, err, out.Errors)
	}
	return out.Document.MD
}

// The create-excel skill's example must convert the way the skill describes.
func TestSkillExampleConverts(t *testing.T) {
	b, err := os.ReadFile("../../../skills/create-excel/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "```markdown\n")
	j := strings.Index(s[i+12:], "```")
	if i < 0 || j < 0 {
		t.Fatal("no ```markdown example in the skill")
	}
	sheets, st := Parse(s[i+12 : i+12+j])
	if st.Sheets != 2 || st.Tables != 3 || st.Ignored != 0 || sheets[0].Name != "Sales 2026" || sheets[1].Name != "Assumptions" {
		t.Fatalf("skill example parsed as %+v (%q, %q)", st, sheets[0].Name, sheets[1].Name)
	}
	// The skill says the first data row is row 2: =B2*C2 must sit in row 2.
	if got := sheets[0].Tables[0][1][3]; got != "=B2*C2" {
		t.Fatalf("row 2 revenue cell = %q", got)
	}
	if _, err := Write(sheets); err != nil {
		t.Fatal(err)
	}
}
