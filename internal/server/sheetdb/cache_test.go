package sheetdb

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func monthBook(t *testing.T, month int, codeAsNumber bool) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"Račun", "Konto", "Iznos", "Status"})
	blue, _ := f.NewStyle(&excelize.Style{Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"BDD7EE"}}})
	for i := 1; i <= 3; i++ {
		var konto any = fmt.Sprintf("0127000%d", i)
		if codeAsNumber {
			konto = 1270000 + i
		}
		_ = f.SetSheetRow("Sheet1", fmt.Sprintf("A%d", i+1), &[]any{fmt.Sprintf("%d-%d-1", i, month), konto, float64(100 * i), "n"})
	}
	_ = f.SetCellStyle("Sheet1", "D3", "D3", blue) // the user's "blue n" = paid
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCacheBuildOpenAssemble(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var parts []Part
	for m := 1; m <= 2; m++ {
		path := filepath.Join(dir, fmt.Sprintf("m%d.sheetdb", m))
		short := fmt.Sprintf("F2 Podaci %02d-2026", m)
		d, err := Build(ctx, Source{Label: "u." + fmt.Sprint(m), Name: "x.xlsx", Short: short, Data: monthBook(t, m, m == 2)}, Options{}, path)
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
		parts = append(parts, Part{Path: path, Label: "u." + fmt.Sprint(m), Short: short})
	}
	// One file from disk: same tables, colours as words, read-only.
	d, err := Open(ctx, parts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := d.Schema()
	if !strings.Contains(s, "TABLE sheet1") || !strings.Contains(s, "status_color  color") || !strings.Contains(s, "light blue #BDD7EE") {
		t.Fatalf("schema from disk:\n%s", s)
	}
	r := q(t, d, `SELECT _row, status, status_color FROM sheet1 WHERE status_color LIKE '%blue%'`)
	if r.Total != 1 || Cell(r.Rows[0][0]) != "3" {
		t.Fatalf("colour query: %v", r.Rows)
	}
	if _, err := d.Query(ctx, `CREATE TABLE x(a)`, 1); err == nil {
		t.Fatal("cache file writable")
	}
	// Two files: tables named after them, a combined view, types reconciled.
	a, err := Assemble(ctx, parts)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s = a.Schema()
	for _, w := range []string{"VIEW f2_podaci_all", "TABLE f2_podaci_01_2026", "TABLE f2_podaci_02_2026", "(same columns as f2_podaci_01_2026; konto is integer here)"} {
		if !strings.Contains(s, w) {
			t.Errorf("assembled schema lacks %q:\n%s", w, s)
		}
	}
	r = q(t, a, `SELECT _file, count(*), sum(iznos), typeof(konto) FROM f2_podaci_all GROUP BY _file ORDER BY _file`)
	if r.Total != 2 || Cell(r.Rows[0][2]) != "600" || r.Rows[1][3] != "text" {
		t.Fatalf("view: %v", r.Rows)
	}
	// The integer account column of file 2 still matches file 1's codes.
	r = q(t, a, `SELECT count(*) FROM f2_podaci_01_2026 a JOIN f2_podaci_02_2026 b ON b.konto = a.konto`)
	if Cell(r.Rows[0][0]) != "3" {
		t.Fatalf("cross-file join: %v", r.Rows)
	}
	if _, err := a.Query(ctx, `ATTACH DATABASE '`+parts[0].Path+`' AS z`, 1); err == nil {
		t.Fatal("ATTACH allowed after assembly")
	}
}

func TestMDTables(t *testing.T) {
	md := "# Račun 901\n\nTekst.\n\n| Stavka | Količina | Iznos |\n|---|---|---|\n| **Usluga A** | 2 | 1.234,50 |\n| Usluga B | 1 | 99,00 |\n\nStranica 2\n\n| Stavka | Količina | Iznos |\n|---|---|---|\n| Usluga C | 3 | 10,00 |\n\n## Rekapitulacija\n\n| Osnovica | PDV |\n|---|---|\n| 1.343,50 | 335,88 |\n"
	d := load(t, []Source{{Label: "u.5", Name: "r.pdf", Format: "md", Text: md}}, Options{})
	s := d.Schema()
	if !strings.Contains(s, "TABLE racun_901") || !strings.Contains(s, "3 data rows") || !strings.Contains(s, "iznos     number") || !strings.Contains(s, "TABLE rekapitulacija") {
		t.Fatalf("schema:\n%s", s)
	}
	r := q(t, d, `SELECT sum(iznos), group_concat(stavka) FROM racun_901`)
	if Cell(r.Rows[0][0]) != "1343.5" || r.Rows[0][1] != "Usluga A,Usluga B,Usluga C" {
		t.Fatalf("md rows: %v", r.Rows)
	}
	if _, err := Load(context.Background(), []Source{{Label: "u.6", Name: "x.pdf", Format: "md", Text: "no tables"}}, Options{}); err == nil || !strings.Contains(err.Error(), "no tables") {
		t.Fatalf("no tables: %v", err)
	}
}

func TestColorWords(t *testing.T) {
	for in, want := range map[string]string{"BDD7EE": "light blue #BDD7EE", "FFFF00": "yellow #FFFF00", "FF0000": "red #FF0000", "FFFFFF": "", "00B050": "green #00B050", "808080": "grey #808080"} {
		if got := colorWords(in, 0); got != want {
			t.Errorf("colorWords(%s) = %q, want %q", in, got, want)
		}
	}
	if got := colorWords("4472C4", 0.8); !strings.HasPrefix(got, "light blue") {
		t.Errorf("tinted accent: %q", got)
	}
}

func TestKey(t *testing.T) {
	for in, want := range map[any]string{"01270000": "1270000", int64(1270000): "1270000", "1/11/01": "1-11-1", " 1-11-1 ": "1-11-1", "ab 0012/x": "AB12-X", "R-0105": "R-105", "0": "0", "2026.11.05": "2026-11-5"} {
		if got := sqlKey(in); got != want {
			t.Errorf("key(%v) = %v, want %s", in, got, want)
		}
	}
	d := load(t, []Source{{Label: "x", Name: "a.csv", Data: []byte("a\n1\n")}}, Options{})
	if r := q(t, d, `SELECT key('001/11/1') = key('1-11-1')`); Cell(r.Rows[0][0]) != "1" {
		t.Fatal("key() in SQL")
	}
}
