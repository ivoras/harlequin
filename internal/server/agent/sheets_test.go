package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/sheetdb"
	"github.com/xuri/excelize/v2"
)

func sheetTestAgent(t *testing.T) (*Agent, *runContext) {
	t.Helper()
	a := &Agent{DataDir: t.TempDir(), Runner: jsrun.New(jsrun.Options{Timeout: 10 * time.Second})}
	rc := &runContext{userID: 1}
	f := excelize.NewFile()
	_ = f.SetSheetName("Sheet1", "Prodaja")
	_ = f.SetSheetRow("Prodaja", "A1", &[]any{"Konto", "Kupac", "Iznos"})
	for i := 0; i < 300; i++ {
		_ = f.SetSheetRow("Prodaja", fmt.Sprintf("A%d", i+2), &[]any{fmt.Sprintf("0%d", 1270000+i%3), fmt.Sprintf("Kupac %d", i%5), float64(i)})
	}
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.tmpRoot(1).Write("prodaja.xlsx", b.Bytes()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, e := range a.sheets.entries {
			if e.db != nil {
				e.db.Close()
			}
		}
	})
	return a, rc
}

func TestQuerySheetTool(t *testing.T) {
	a, rc := sheetTestAgent(t)
	ctx := context.Background()
	run := func(args map[string]any) string {
		t.Helper()
		out, err := a.querySheetEntry().handler(ctx, rc, args)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(map[string]any{"doc": "tmp://prodaja.xlsx"})
	for _, want := range []string{"TABLE prodaja", "300 data rows", "konto  code", "Example queries:", "SELECT konto, SUM(iznos) AS total"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview lacks %q:\n%s", want, out)
		}
	}
	out = run(map[string]any{"doc": "tmp://prodaja.xlsx", "sql": "SELECT kupac, SUM(iznos) AS s FROM prodaja GROUP BY kupac ORDER BY s DESC LIMIT 1"})
	if !strings.Contains(out, "1 row(s)") || !strings.Contains(out, "| Kupac 4 | 9090 |") {
		t.Fatalf("query:\n%s", out)
	}
	out = run(map[string]any{"doc": "tmp://prodaja.xlsx", "sql": "SELECT * FROM prodaja"})
	if !strings.Contains(out, "300 rows; the first 200 are shown") || !strings.Contains(out, "100 more rows not shown") {
		t.Fatalf("cap:\n%.300s", out)
	}
	out = run(map[string]any{"doc": "tmp://prodaja.xlsx", "sql": "SELECT customer FROM prodaja"})
	if !strings.Contains(out, "no such column") || !strings.Contains(out, "prodaja(_row, konto, kupac, iznos)") {
		t.Fatalf("error:\n%s", out)
	}
	out = run(map[string]any{"doc": "tmp://prodaja.xlsx", "sql": "SELECT konto, SUM(iznos) AS s FROM prodaja GROUP BY konto", "save_to": "tmp://po_kontu.md"})
	if !strings.Contains(out, "Saved all 3 rows") || !strings.Contains(out, `create_excel with source "tmp://po_kontu.md"`) {
		t.Fatalf("save_to: %s", out)
	}
	md, _ := a.tmpRoot(1).Read("po_kontu.md")
	if !strings.Contains(string(md), "# po_kontu\n") || !strings.Contains(string(md), "| 01270000 | 14850 |") {
		t.Fatalf("saved file:\n%s", md)
	}
	if out := run(map[string]any{"doc": "tmp://po_kontu.md"}); !strings.Contains(out, "is not a spreadsheet") {
		t.Fatalf("non-spreadsheet: %s", out)
	}
	if len(a.sheets.entries) != 1 {
		t.Fatalf("%d cache entries, want 1 (same file reused)", len(a.sheets.entries))
	}
}

func TestRunJSSheetHelpers(t *testing.T) {
	a, rc := sheetTestAgent(t)
	ctx := context.Background()
	rcx := a.jsRunContext(ctx, rc)
	rcx.Globals = a.sheetJSGlobals(ctx, rc)
	if _, err := a.Runner.Run(`read_doc("tmp://missing.txt")`, rcx); err == nil {
		t.Fatal("read_doc of a missing file should throw")
	}
	_ = a.tmpRoot(1).Write("po.txt", []byte("hello"))
	res, err := a.Runner.Run(`
var rows = sheets.query("tmp://prodaja.xlsx", "SELECT konto, COUNT(*) AS n FROM prodaja GROUP BY konto ORDER BY konto");
println(rows.length, rows[0].konto, rows[0].n, JSON.stringify(rows[2]));
var t = sheets.tables("tmp://prodaja.xlsx");
println(t[0].table, t[0].rows, t[0].columns[0].type);
println(read_doc("tmp://po.txt"));
try { sheets.query("tmp://prodaja.xlsx", "DELETE FROM prodaja"); } catch (e) { println("denied"); }
try { sheets.query("tmp://prodaja.xlsx", "SELECT customer FROM prodaja"); } catch (e) { println(String(e).indexOf("prodaja(_row, konto, kupac, iznos)") >= 0); }`, rcx)
	if err != nil {
		t.Fatal(err)
	}
	want := "3 01270000 100 {\"konto\":\"01270002\",\"n\":100}\nprodaja 300 code\nhello\ndenied\ntrue\n"
	if res.Output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", res.Output, want)
	}
}

func TestSheetCacheExpiry(t *testing.T) {
	var c sheetCache
	loads := 0
	load := func() (*sheetdb.DB, error) {
		loads++
		return sheetdb.Load(context.Background(), []sheetdb.Source{{Label: "x", Name: "x.csv", Data: []byte("a\n1\n")}}, sheetdb.Options{})
	}
	if _, err := c.get("k", load); err != nil {
		t.Fatal(err)
	}
	_, _ = c.get("k", load)
	if loads != 1 {
		t.Fatalf("%d loads, want 1", loads)
	}
	// Used a minute ago but loaded over a week ago: reloaded.
	c.entries["k"].loaded = time.Now().Add(-sheetCacheMaxAge - time.Hour)
	_, _ = c.get("k", load)
	if loads != 2 {
		t.Fatalf("%d loads after a week, want 2", loads)
	}
	// Idle past the limit: dropped when another workbook is fetched.
	c.entries["k"].used = time.Now().Add(-sheetCacheIdle - time.Minute)
	_, _ = c.get("other", load)
	if _, ok := c.entries["k"]; ok {
		t.Fatal("idle entry kept")
	}
	for i := range sheetCacheSize + 2 {
		_, _ = c.get(fmt.Sprint(i), load)
	}
	if len(c.entries) > sheetCacheSize {
		t.Fatalf("%d entries, cap %d", len(c.entries), sheetCacheSize)
	}
	for _, e := range c.entries {
		e.db.Close()
	}
}

// A big uploaded workbook: read_doc steers to query_sheet with a short
// preview, and query_sheet reads the stored original by document reference.
func TestBigSheetUpload(t *testing.T) {
	h := newGenHarness(t)
	f := excelize.NewFile()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"Konto", "Opis", "Iznos"})
	for i := 0; i < 2000; i++ {
		_ = f.SetSheetRow("Sheet1", fmt.Sprintf("A%d", i+2), &[]any{fmt.Sprintf("0%d", 1000000+i), "Neki dulji opis stavke", i})
	}
	b, _ := f.WriteToBuffer()
	id := h.uploadXLSX("bilanca.xlsx", b.Bytes())
	t.Cleanup(func() {
		for _, e := range h.a.sheets.entries {
			if e.db != nil {
				e.db.Close()
			}
		}
	})
	out := h.call("read_doc", map[string]any{"doc": ref(id)})
	if !strings.Contains(out, "do NOT read it page by page") || !strings.Contains(out, "characters 0–4000 of") {
		t.Fatalf("read_doc:\n%.600s", out)
	}
	if out := h.call("read_doc", map[string]any{"doc": ref(id), "limit": float64(100)}); !strings.Contains(out, "characters 0–100 of") {
		t.Fatalf("explicit limit not kept: %.300s", out)
	}
	out = h.call("query_sheet", map[string]any{"doc": ref(id), "sql": "SELECT COUNT(*), SUM(iznos), MIN(konto) FROM sheet1"})
	if !strings.Contains(out, "| 2000 | 1999000 | 01000000 |") {
		t.Fatalf("query_sheet by reference:\n%s", out)
	}
}
