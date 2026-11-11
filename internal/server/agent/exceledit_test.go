package agent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mdconv"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// workbookBytes is an invented two-sheet workbook with a style, formulas and a merged title.
func workbookBytes(t *testing.T) []byte {
	t.Helper()
	f := excelize.NewFile()
	f.SetSheetName("Sheet1", "October")
	f.NewSheet("Notes")
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetCellValue("October", "A1", "Payroll October")
	f.MergeCell("October", "A1", "C1")
	f.SetSheetRow("October", "A2", &[]any{"Name", "Gross", "Net"})
	f.SetCellStyle("October", "A2", "C2", bold)
	f.SetSheetRow("October", "A3", &[]any{"Ana Horvat", 1620})
	f.SetCellFormula("October", "C3", "B3*0.73")
	f.SetSheetRow("October", "A4", &[]any{"Ivan Kovač", 1500})
	f.SetCellFormula("October", "C4", "B4*0.73")
	f.SetCellValue("Notes", "A1", "Travel")
	b, err := f.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// uploadXLSX stores a workbook the way an upload does: ingested with its
// converted text, the original file kept, no generation sidecar.
func (h *genHarness) uploadXLSX(title string, data []byte) int64 {
	h.t.Helper()
	md, err := mdconv.Convert(title, data)
	if err != nil {
		h.t.Fatal(err)
	}
	doc, err := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: title, URI: "upload://" + title, Mime: xlsxMime, Content: md}, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	h.a.storeDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, data, ".xlsx")
	return doc.ID
}

func TestReadDocCellsGrid(t *testing.T) {
	h := newGenHarness(t)
	id := h.uploadXLSX("payroll.xlsx", workbookBytes(t))
	out := h.call("read_doc", map[string]any{"doc": ref(id), "cells": true})
	for _, want := range []string{`## Sheet "October" (A1:C4)`, "|   | A | B | C |", "| 3 | Ana Horvat | 1620 | =B3*0.73 |", "Merged: A1:C1", `## Sheet "Notes"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// the usual rendering is unchanged: no sheet headings, no addresses
	plain := h.call("read_doc", map[string]any{"doc": ref(id)})
	if strings.Contains(plain, `Sheet "October"`) || strings.Contains(plain, "|   | A |") {
		t.Fatalf("default read_doc changed:\n%s", plain)
	}
	// the usual character window applies
	win := h.call("read_doc", map[string]any{"doc": ref(id), "cells": true, "offset": float64(10), "limit": float64(40)})
	if !strings.Contains(win, "characters 10–50 of") || !strings.Contains(win, "offset=50") {
		t.Fatalf("window: %s", win)
	}
	// anything that is not an .xlsx says so
	text, _ := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: "n", URI: "x", Mime: "text/markdown", Content: "hello"}, 1)
	if out := h.call("read_doc", map[string]any{"doc": ref(text.ID), "cells": true}); !strings.Contains(out, "exists only for Excel") {
		t.Fatal(out)
	}
	if out := h.call("read_doc", map[string]any{"doc": "tmp://a.md", "cells": true}); !strings.Contains(out, "exists only for Excel") {
		t.Fatal(out)
	}
}

func TestEditUploadedExcel(t *testing.T) {
	h := newGenHarness(t)
	orig := workbookBytes(t)
	id := h.uploadXLSX("payroll.xlsx", orig)
	path := h.stored(id)

	out := h.call("edit_doc", map[string]any{"doc": ref(id), "sheet": "October", "cells": map[string]any{"B3": 1700.0, "C3": "=B3*0.8"}})
	if !strings.HasPrefix(out, "Edited Excel workbook") || !strings.Contains(out, "set 2 cell(s) on sheet October") || !strings.Contains(out, "formatting, formulas and other sheets were kept") && !strings.Contains(out, "formatting, formulas") {
		t.Fatal(out)
	}
	first := readFile(t, path)
	if bytes.Equal(first, orig) {
		t.Fatal("stored file unchanged")
	}
	wb, _ := excelize.OpenReader(bytes.NewReader(first))
	if v, _ := wb.GetCellValue("October", "B3"); v != "1700" {
		t.Fatalf("B3 = %q", v)
	}
	if fo, _ := wb.GetCellFormula("October", "C3"); fo != "B3*0.8" {
		t.Fatalf("C3 formula %q", fo)
	}
	if v, _ := wb.GetCellValue("Notes", "A1"); v != "Travel" {
		t.Fatal("other sheet changed")
	}
	if txt := h.text(id); !strings.Contains(txt, "1700") || strings.Contains(txt, "1620") {
		t.Fatalf("index: %q", txt)
	}

	// a second edit on top, then undo goes back exactly one step
	out = h.call("edit_doc", map[string]any{"doc": ref(id), "sheet": "October", "append": "| Marta Babić | 1800 |"})
	if !strings.HasPrefix(out, "Edited Excel workbook") || !strings.Contains(h.text(id), "Marta") {
		t.Fatal(out)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "Restored") {
		t.Fatal(out)
	}
	if !bytes.Equal(readFile(t, path), first) {
		t.Fatal("undo did not restore the file")
	}
	if txt := h.text(id); strings.Contains(txt, "Marta") || !strings.Contains(txt, "1700") {
		t.Fatalf("index after undo: %q", txt)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "no previous version") {
		t.Fatal(out)
	}

	// replace, insert by quoted row, and refusals that change nothing
	h.call("edit_doc", map[string]any{"doc": ref(id), "old_string": "Travel", "new_string": "Travel costs"})
	h.call("edit_doc", map[string]any{"doc": ref(id), "sheet": "October", "insert": "| Eva Novak | 1400 |", "before": "Ivan Kovač"})
	txt := h.text(id)
	if !strings.Contains(txt, "Travel costs") || !strings.Contains(txt, "Eva Novak") {
		t.Fatalf("index: %q", txt)
	}
	before := readFile(t, path)
	for name, args := range map[string]map[string]any{
		"diff":         {"diff": "@@ -1 +1 @@\n-a\n+b\n"},
		"two ops":      {"cells": map[string]any{"B3": 1}, "append": "| a |"},
		"bad address":  {"cells": map[string]any{"B": 1}, "sheet": "October"},
		"no sheet":     {"cells": map[string]any{"B3": 1}},
		"unknown":      {"cells": map[string]any{"Nope!B3": 1}},
		"merged":       {"cells": map[string]any{"B1": 1}, "sheet": "October"},
		"not a table":  {"append": "some words", "sheet": "October"},
		"cells as str": {"cells": "B3=1"},
	} {
		args["doc"] = ref(id)
		if out := h.call("edit_doc", args); !strings.HasPrefix(out, "error:") {
			t.Errorf("%s: %s", name, out)
		}
	}
	if !bytes.Equal(before, readFile(t, path)) {
		t.Fatal("a refused edit changed the file")
	}
}

func TestCellsRefusedOnOtherDocuments(t *testing.T) {
	h := newGenHarness(t)
	gid, _ := h.create("create_excel", "Gen", sheetMD)
	if out := h.call("edit_doc", map[string]any{"doc": ref(gid), "cells": map[string]any{"B2": 1}}); !strings.Contains(out, "generated from Markdown; change it with replace, insert or append") {
		t.Fatal(out)
	}
	wid := h.upload("w.docx", tinyDocx(t, `<w:p><w:r><w:t>Alpha.</w:t></w:r></w:p>`))
	if out := h.call("edit_doc", map[string]any{"doc": ref(wid), "cells": map[string]any{"B2": 1}}); !strings.Contains(out, "cells only works on an uploaded Excel") {
		t.Fatal(out)
	}
	doc, _ := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: "n", URI: "x", Mime: "text/markdown", Content: "one\n"}, 1)
	h.a.storeSavedDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, "one\n")
	if out := h.call("edit_doc", map[string]any{"doc": ref(doc.ID), "cells": map[string]any{"B2": 1}}); !strings.Contains(out, "cells only works on an uploaded Excel") {
		t.Fatal(out)
	}
	if out := h.call("edit_file", map[string]any{"path": "tmp://x.md", "cells": map[string]any{"B2": 1}, "append": "x"}); !strings.HasPrefix(out, "error:") {
		t.Fatal(out)
	}
	// not a real workbook
	bad := h.uploadXLSX2("bad.xlsx", []byte("not a workbook"))
	if out := h.call("edit_doc", map[string]any{"doc": ref(bad), "cells": map[string]any{"B2": 1}}); !strings.Contains(out, "not a readable .xlsx") {
		t.Fatal(out)
	}
}

// uploadXLSX2 stores arbitrary bytes under the Excel MIME type.
func (h *genHarness) uploadXLSX2(title string, data []byte) int64 {
	doc, err := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: title, URI: "upload://" + title, Mime: xlsxMime, Content: "x"}, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	h.a.storeDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, data, ".xlsx")
	return doc.ID
}
