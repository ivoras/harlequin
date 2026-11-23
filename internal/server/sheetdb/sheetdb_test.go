package sheetdb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/charmap"
)

// testBook: a title row and a blank row above the header, a date column, an
// account code kept as text with its leading zero, an amount written as text,
// a keyword header and a second (hidden) sheet.
func testBook(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	sh := "Prodaja 2026"
	if err := f.SetSheetName("Sheet1", sh); err != nil {
		t.Fatal(err)
	}
	date, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	set := func(cell string, v any) {
		if err := f.SetCellValue(sh, cell, v); err != nil {
			t.Fatal(err)
		}
	}
	set("A1", "Acme d.o.o. — prodaja")
	for i, h := range []string{"Šifra konta", "Kupac", "Datum", "Iznos (EUR)", "Order", "Iznos tekst"} {
		set(fmt.Sprintf("%c3", 'A'+i), h)
	}
	rows := []struct {
		code, buyer string
		day         int
		amount      float64
		txt         string
	}{{"01270000", "Ana Horvat", 21, 16500, "1.234,50"}, {"01710000", "Ivan Kovač", 30, 2150.25, "12,5"}, {"01890000", "Ana Horvat", 30, 870.99, "x"}}
	for i, r := range rows {
		n := i + 4
		set(fmt.Sprintf("A%d", n), r.code)
		set(fmt.Sprintf("B%d", n), r.buyer)
		set(fmt.Sprintf("C%d", n), time.Date(2026, 1, r.day, 0, 0, 0, 0, time.UTC))
		_ = f.SetCellStyle(sh, fmt.Sprintf("C%d", n), fmt.Sprintf("C%d", n), date)
		set(fmt.Sprintf("D%d", n), r.amount)
		set(fmt.Sprintf("E%d", n), i+1)
		set(fmt.Sprintf("F%d", n), r.txt)
	}
	set("G4", "1.500,00")
	set("G5", "2.250,50 €")
	set("G6", "(12,00)")
	set("H5", "stray") // wider than the header: gets a letter-named column
	if _, err := f.NewSheet("Konta"); err != nil {
		t.Fatal(err)
	}
	_ = f.SetCellValue("Konta", "A1", "Konto")
	_ = f.SetCellValue("Konta", "B1", "Naziv")
	_ = f.SetCellValue("Konta", "A2", "01270000")
	_ = f.SetCellValue("Konta", "B2", "Licence")
	pad, _ := f.NewStyle(&excelize.Style{CustomNumFmt: ptr("00000000")})
	_ = f.SetCellValue("Konta", "A3", 1710000) // a number shown as 01710000
	_ = f.SetCellStyle("Konta", "A3", "A3", pad)
	_ = f.SetCellValue("Konta", "B3", "Najam")
	_ = f.SetCellValue("Konta", "A4", 1890000) // a bare number among text codes: its zero is gone
	_ = f.SetCellValue("Konta", "B4", "Oprema")
	_ = f.SetSheetVisible("Konta", false)
	// A sheet where the account is a plain integer column.
	_, _ = f.NewSheet("Bilanca")
	_ = f.SetCellValue("Bilanca", "A1", "Konto")
	_ = f.SetCellValue("Bilanca", "B1", "Saldo")
	_ = f.SetCellValue("Bilanca", "A2", 1270000)
	_ = f.SetCellValue("Bilanca", "B2", 100.5)
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func load(t *testing.T, srcs []Source, opt Options) *DB {
	t.Helper()
	d, err := Load(context.Background(), srcs, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func q(t *testing.T, d *DB, sql string) *Result {
	t.Helper()
	r, err := d.Query(context.Background(), sql, 100)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return r
}

func TestLoadXLSX(t *testing.T) {
	d := load(t, []Source{{Label: `u.1 "Prodaja.xlsx"`, Name: "Prodaja.xlsx", Data: testBook(t)}}, Options{})
	s := d.Schema()
	for _, want := range []string{
		`TABLE prodaja_2026 — sheet "Prodaja 2026"`, "3 data rows", "header is spreadsheet row 3",
		"row above the header: Acme d.o.o. — prodaja",
		`sifra_konta  code`, `"01270000"`, `kupac`, `datum        date`, `"2026-01-21"`,
		`iznos_eur    number`, `order_       integer`, `iznos_tekst  text`, `h            text`, `g            text-number`,
		`TABLE konta`, "hidden sheet",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schema lacks %q:\n%s", want, s)
		}
	}
	r := q(t, d, `SELECT kupac, SUM(iznos_eur) AS total, MIN(datum) AS first FROM prodaja_2026 GROUP BY kupac ORDER BY total DESC`)
	if got := Markdown(r.Columns, r.Rows, 0); !strings.Contains(got, "| Ana Horvat | 17370.99 | 2026-01-21 |") || !strings.Contains(got, "| Ivan Kovač | 2150.25 | 2026-01-30 |") {
		t.Fatalf("group by:\n%s", got)
	}
	r = q(t, d, `SELECT _row, sifra_konta, num(iznos_tekst) FROM prodaja_2026 ORDER BY _row`)
	if Cell(r.Rows[0][0]) != "4" || Cell(r.Rows[0][1]) != "01270000" || Cell(r.Rows[0][2]) != "1234.5" || Cell(r.Rows[1][2]) != "12.5" || r.Rows[2][2] != nil {
		t.Fatalf("rows: %v", r.Rows)
	}
	// Join across sheets.
	r = q(t, d, `SELECT k.naziv FROM prodaja_2026 p JOIN konta k ON k.konto = p.sifra_konta ORDER BY k._row`)
	if r.Total != 2 || r.Rows[0][0] != "Licence" || r.Rows[1][0] != "Najam" {
		t.Fatalf("join: %v", r.Rows)
	}
}

// Account codes keep their leading zeros, and one column never mixes numbers
// and text (which SQLite would never find equal).
func TestAccountCodes(t *testing.T) {
	d := load(t, []Source{{Label: "u.1", Name: "a.xlsx", Data: testBook(t)}}, Options{})
	r := q(t, d, `SELECT konto, typeof(konto) FROM konta ORDER BY _row`)
	want := [][2]string{{"01270000", "text"}, {"01710000", "text"}, {"1890000", "text"}}
	for i, w := range want {
		if Cell(r.Rows[i][0]) != w[0] || r.Rows[i][1] != w[1] {
			t.Fatalf("row %d: %v, want %v", i, r.Rows[i], w)
		}
	}
	if s := d.Schema(); !strings.Contains(s, "konto  code") || !strings.Contains(s, "1 of these codes were numbers") {
		t.Fatalf("schema:\n%s", s)
	}
	// Text code = text literal, and the bare number is found as text too.
	if r := q(t, d, `SELECT naziv FROM konta WHERE konto = '01710000' OR konto = '1890000'`); r.Total != 2 {
		t.Fatalf("text match: %v", r.Rows)
	}
	// An integer account column joins a code column numerically.
	if r := q(t, d, `SELECT k.naziv, b.saldo FROM bilanca b JOIN konta k ON k.konto = b.konto`); r.Total != 1 || r.Rows[0][0] != "Licence" {
		t.Fatalf("integer-to-code join: %v", r.Rows)
	}
	// The documented way to match a code that lost its zero.
	if r := q(t, d, `SELECT count(*) FROM konta WHERE ltrim(konto, '0') = '1890000'`); Cell(r.Rows[0][0]) != "1" {
		t.Fatal("ltrim match")
	}
}

func ptr[T any](v T) *T { return &v }

func TestReadOnly(t *testing.T) {
	d := load(t, []Source{{Label: "u.1", Name: "a.xlsx", Data: testBook(t)}}, Options{})
	for _, bad := range []string{
		`DROP TABLE konta`, `INSERT INTO konta VALUES (1, 'a', 'b')`, `UPDATE konta SET naziv = 'x'`,
		`ATTACH DATABASE '/tmp/x.db' AS x`, `PRAGMA table_info(konta)`, `SELECT load_extension('x')`,
		`CREATE TABLE z(a)`, `VACUUM INTO '/tmp/out.db'`,
	} {
		if _, err := d.Query(context.Background(), bad, 10); err == nil {
			t.Errorf("%s: allowed", bad)
		}
	}
	if r := q(t, d, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 5) SELECT count(*) FROM n`); Cell(r.Rows[0][0]) != "5" {
		t.Fatal("recursive CTE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := d.Query(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n) SELECT count(*) FROM n`, 10); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("runaway query: %v", err)
	}
}

func TestOptions(t *testing.T) {
	data := testBook(t)
	d := load(t, []Source{{Label: "u.1", Name: "a.xlsx", Data: data}}, Options{Sheet: "konta", HeaderRow: -1})
	if len(d.Tables) != 1 || d.Tables[0].Columns[0].Name != "a" || d.Tables[0].Rows != 4 {
		t.Fatalf("tables: %+v", d.Tables[0])
	}
	if _, err := Load(context.Background(), []Source{{Label: "u.1", Name: "a.xlsx", Data: data}}, Options{Sheet: "nope"}); err == nil || !strings.Contains(err.Error(), "Prodaja 2026, Konta") {
		t.Fatalf("missing sheet: %v", err)
	}
	if _, err := Load(context.Background(), []Source{{Label: "u.1", Name: "a.xlsx", Data: data}}, Options{HeaderRow: 99}); err == nil {
		t.Fatal("bad header_row accepted")
	}
}

func TestCSV(t *testing.T) {
	text := "Šifra;Opis;Iznos\n0127;Licence;1500\n0171;Najam;1.234,50\n"
	win, _ := charmap.Windows1250.NewEncoder().String(text)
	d := load(t, []Source{{Label: "u.2", Name: "data/izvoz.csv", Data: []byte(win)}, {Label: "u.3", Name: "b.csv", Data: []byte("a,b\n1,2\n")}}, Options{})
	if d.Tables[0].Name != "izvoz" || d.Tables[1].Name != "b" {
		t.Fatalf("names: %s %s", d.Tables[0].Name, d.Tables[1].Name)
	}
	r := q(t, d, `SELECT sifra, sum(num(iznos)) FROM izvoz`)
	if Cell(r.Rows[0][1]) != "2734.5" {
		t.Fatalf("csv sum: %v", r.Rows)
	}
	r = q(t, d, `SELECT typeof(sifra) FROM izvoz`)
	if r.Rows[0][0] != "text" {
		t.Fatal("leading-zero code became a number")
	}
}

func TestParseNum(t *testing.T) {
	for in, want := range map[string]float64{
		"1.234,56": 1234.56, "1,234.56": 1234.56, "1 234,56 €": 1234.56, "(123)": -123, "-12,5%": -12.5,
		"12-": -12, "1.234.567": 1234567, "1,5": 1.5, "0.25": 0.25, "+7": 7, "1.234": 1.234, "EUR 10": 10,
	} {
		if got, ok := parseNum(in); !ok || got != want {
			t.Errorf("parseNum(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "abc", "1.2.3,4,5", "12a", "-"} {
		if _, ok := parseNum(in); ok {
			t.Errorf("parseNum(%q) accepted", in)
		}
	}
}

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{
		"Iznos (EUR)": "iznos_eur", "Šifra": "sifra", "Đak": "djak", "2024": "c_2024", "Order": "order_", "  ": "",
		"Naziv partnera / kupca": "naziv_partnera_kupca",
	} {
		if got := ident(in, ""); got != want {
			t.Errorf("ident(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDates(t *testing.T) {
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	for v, want := range map[float64]string{46043: "2026-01-21", 46043.5: "2026-01-21 12:00:00", 0.25: "06:00:00", 1: "1900-01-01", 61: "1900-03-01"} {
		if got := string(serialDate(v, epoch)); got != want {
			t.Errorf("serialDate(%v) = %s, want %s", v, got, want)
		}
	}
	for code, want := range map[string]bool{"dd.mm.yyyy": true, "#,##0.00": false, `#,##0 "kn"`: false, "[h]:mm": true, "[Red]0.00": false, "0%": false, `[$-41A]d. mmmm yyyy`: true} {
		if isDateFormat(code) != want {
			t.Errorf("isDateFormat(%q) != %v", code, want)
		}
	}
}

// SHEETDB_BENCH=1: load a 1M-cell sheet and time a query.
func TestBigLoad(t *testing.T) {
	if os.Getenv("SHEETDB_BENCH") == "" {
		t.Skip("set SHEETDB_BENCH=1")
	}
	f := excelize.NewFile()
	sw, _ := f.NewStreamWriter("Sheet1")
	_ = sw.SetRow("A1", []any{"id", "kupac", "iznos", "opis", "a", "b", "c", "d", "e", "f"})
	for i := 2; i <= 100_001; i++ {
		cell, _ := excelize.CoordinatesToCellName(1, i)
		_ = sw.SetRow(cell, []any{i, fmt.Sprintf("Kupac %d", i%97), float64(i) * 1.5, "neki opis", 1, 2, 3, 4, 5, 6})
	}
	_ = sw.Flush()
	b, _ := f.WriteToBuffer()
	st := time.Now()
	d := load(t, []Source{{Label: "u.1", Name: "big.xlsx", Data: b.Bytes()}}, Options{})
	t.Logf("xlsx %d KB, load %v", b.Len()>>10, time.Since(st))
	st = time.Now()
	r := q(t, d, `SELECT kupac, sum(iznos) FROM sheet1 GROUP BY kupac ORDER BY 2 DESC LIMIT 3`)
	t.Logf("query %v: %v", time.Since(st), r.Rows)
}
