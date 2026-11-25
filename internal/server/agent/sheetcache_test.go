package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
	"github.com/xuri/excelize/v2"
)

func ledger(t *testing.T, month int) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetSheetRow("Sheet1", "A1", &[]any{"Račun", "Iznos"})
	for i := 1; i <= 3; i++ {
		_ = f.SetSheetRow("Sheet1", fmt.Sprintf("A%d", i+1), &[]any{fmt.Sprintf("%d-%d-1", i, month), float64(10 * i)})
	}
	b, _ := f.WriteToBuffer()
	return b.Bytes()
}

func sheetCaches(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*"+documents.SheetCacheInfix+"*"))
	return m
}

func TestSheetDiskCache(t *testing.T) {
	h := newGenHarness(t)
	jan := h.uploadXLSX("F2 Podaci 01-2026.xlsx", ledger(t, 1))
	feb := h.uploadXLSX("F2 Podaci 02-2026.xlsx", ledger(t, 2))
	closeAll := func(a *Agent) {
		for _, e := range a.sheets.entries {
			if e.db != nil {
				e.db.Close()
			}
		}
	}
	defer closeAll(h.a)

	if out := h.call("query_sheet", map[string]any{"doc": ref(jan), "sql": "SELECT sum(iznos) FROM f2_podaci_01_2026"}); !strings.Contains(out, "| 60 |") {
		t.Fatalf("first query: %s", out)
	}
	caches := sheetCaches(t, h.files)
	if len(caches) != 1 {
		t.Fatalf("cache files after first query: %v", caches)
	}
	// A new agent (a restart, another session): served from the file.
	fresh := &Agent{DataDir: h.a.DataDir, Docs: h.a.Docs, Storage: h.a.Storage}
	h.a = fresh
	defer closeAll(fresh)
	if out := h.call("query_sheet", map[string]any{"doc": ref(jan), "sql": "SELECT count(*) FROM f2_podaci_01_2026"}); !strings.Contains(out, "| 3 |") {
		t.Fatalf("from cache: %s", out)
	}
	// Older than a week: rebuilt (the file is new again).
	old := time.Now().Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(caches[0], old, old)
	closeAll(fresh)
	fresh.sheets = sheetCache{}
	h.call("query_sheet", map[string]any{"doc": ref(jan)})
	if fi, _ := os.Stat(caches[0]); time.Since(fi.ModTime()) > time.Hour {
		t.Fatal("week-old cache not rebuilt")
	}
	// Two documents: named after the files, one combined view.
	out := h.call("query_sheet", map[string]any{"doc": ref(jan), "more_docs": []any{ref(feb)}})
	for _, w := range []string{"VIEW f2_podaci_all", "TABLE f2_podaci_01_2026", "TABLE f2_podaci_02_2026"} {
		if !strings.Contains(out, w) {
			t.Errorf("two documents lack %q:\n%s", w, out)
		}
	}
	out = h.call("query_sheet", map[string]any{"doc": ref(jan), "more_docs": []any{ref(feb)}, "sql": "SELECT _file, sum(iznos) FROM f2_podaci_all GROUP BY _file ORDER BY _file"})
	if !strings.Contains(out, "| F2 Podaci 01-2026 | 60 |") || !strings.Contains(out, "| F2 Podaci 02-2026 | 60 |") {
		t.Fatalf("view query: %s", out)
	}
	if n := len(sheetCaches(t, h.files)); n != 2 {
		t.Fatalf("%d cache files, want one per document", n)
	}
	// Tables of a PDF (its converted text) are queryable too.
	pdf, err := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: "racun.pdf", URI: "upload://racun.pdf", Mime: pdfMime,
		Content: "# Račun\n\n| Račun | Iznos |\n|---|---|\n| 2-1-1 | 20,00 |\n| 9-9-9 | 5,00 |\n"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	out = h.call("query_sheet", map[string]any{"doc": ref(jan), "more_docs": []any{ref(pdf.ID)},
		"sql": "SELECT p.racun FROM racun p LEFT JOIN f2_podaci_01_2026 l ON key(l.racun) = key(p.racun) WHERE l.racun IS NULL"})
	if !strings.Contains(out, "1 row(s)") || !strings.Contains(out, "| 9-9-9 |") {
		t.Fatalf("pdf table join: %s", out)
	}
	// Deleting the document deletes its cache.
	if err := h.store.DeleteFrom(h.ctx, h.db, h.files, jan); err != nil {
		t.Fatal(err)
	}
	for _, c := range sheetCaches(t, h.files) {
		if strings.Contains(c, fmt.Sprintf("%d-", jan)) && strings.HasPrefix(filepath.Base(c), fmt.Sprintf("%d-", jan)) {
			t.Fatalf("cache of deleted document kept: %s", c)
		}
	}
	// The weekly sweep.
	for _, c := range sheetCaches(t, h.files) {
		_ = os.Chtimes(c, old, old)
	}
	h.a.SweepSheetCaches()
	if left := sheetCaches(t, h.files); len(left) != 0 {
		t.Fatalf("sweep left %v", left)
	}
}

func TestPrebuildSheetCache(t *testing.T) {
	h := newGenHarness(t)
	id := h.uploadXLSX("a.xlsx", ledger(t, 1))
	stored, _, _, _ := h.store.StoredFile(h.ctx, h.db, id)
	h.a.PrebuildSheetCache(filepath.Join(h.files, stored), xlsxMime)
	if n := len(sheetCaches(t, h.files)); n != 1 {
		t.Fatalf("%d cache files after prebuild", n)
	}
}
