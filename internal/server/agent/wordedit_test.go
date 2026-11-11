package agent

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/docxextract"
	"github.com/ivoras/harlequin/internal/server/mdword"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const wns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// tinyDocx is a hand-written Word file with a header part, split runs and a
// heading style whose id is not the English one. Invented content.
func tinyDocx(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8"?><w:document ` + wns + `><w:body>` + body + `<w:sectPr/></w:body></w:document>`},
		{"word/styles.xml", `<w:styles ` + wns + `><w:style w:styleId="Naslov1"><w:name w:val="heading 1"/></w:style></w:styles>`},
		{"word/header1.xml", `<w:hdr ` + wns + `><w:p><w:r><w:t>Header words</w:t></w:r></w:p></w:hdr>`},
	} {
		w, _ := zw.Create(f[0])
		w.Write([]byte(f[1]))
	}
	zw.Close()
	return buf.Bytes()
}

func zipEntry(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
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

// upload stores a .docx the way an upload does: ingested with its extracted
// text, the original file kept, no generation sidecar.
func (h *genHarness) upload(title string, data []byte) int64 {
	h.t.Helper()
	text, err := docxextract.Text(data)
	if err != nil {
		h.t.Fatal(err)
	}
	doc, err := h.store.IngestInto(h.ctx, h.db, types.CreateDocumentRequest{Title: title, URI: "upload://" + title, Mime: docxMime, Content: text}, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	h.a.storeDocFile(h.ctx, h.db, documents.ScopePersonal, h.rc, doc, data, ".docx")
	return doc.ID
}

func TestEditUploadedWordDocument(t *testing.T) {
	h := newGenHarness(t)
	body := `<w:p><w:pPr><w:pStyle w:val="Naslov1"/></w:pPr><w:r><w:t>Service terms</w:t></w:r></w:p>` +
		`<w:p><w:r><w:rPr><w:b/></w:rPr><w:t xml:space="preserve">The notice period is </w:t></w:r><w:r><w:t>30</w:t></w:r><w:r><w:t xml:space="preserve"> days.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Payment follows within a month.</w:t></w:r></w:p>`
	orig := tinyDocx(t, body)
	id := h.upload("terms.docx", orig)
	path := h.stored(id)

	out := h.call("edit_doc", map[string]any{"doc": ref(id), "old_string": "notice period is 30 days", "new_string": "notice period is 60 days"})
	if !strings.HasPrefix(out, "Edited Word document") || !strings.Contains(out, "original formatting was kept") {
		t.Fatal(out)
	}
	first := readFile(t, path)
	if bytes.Equal(first, orig) || !strings.Contains(zipEntry(t, first, "word/document.xml"), "60") {
		t.Fatal("stored file not changed")
	}
	if zipEntry(t, first, "word/header1.xml") != zipEntry(t, orig, "word/header1.xml") || zipEntry(t, first, "word/styles.xml") != zipEntry(t, orig, "word/styles.xml") {
		t.Fatal("untouched parts changed")
	}
	if txt := h.text(id); !strings.Contains(txt, "notice period is 60 days") || strings.Contains(txt, "30") {
		t.Fatalf("index: %q", txt)
	}

	// A second edit builds on the first, and undo goes back one step only.
	out = h.call("edit_doc", map[string]any{"doc": ref(id), "insert": "## Refunds\n\nNo refunds after use.", "after": "Payment follows within a month."})
	if !strings.HasPrefix(out, "Edited Word document") {
		t.Fatal(out)
	}
	if txt := h.text(id); !strings.Contains(txt, "No refunds after use.") || !strings.Contains(txt, "60 days") {
		t.Fatalf("index: %q", txt)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "Restored") {
		t.Fatal(out)
	}
	if !bytes.Equal(readFile(t, path), first) {
		t.Fatal("undo did not restore the previous file")
	}
	if txt := h.text(id); strings.Contains(txt, "refunds") || !strings.Contains(txt, "60 days") {
		t.Fatalf("index after undo: %q", txt)
	}
	if out := h.call("edit_doc", map[string]any{"doc": ref(id), "undo": true}); !strings.Contains(out, "no previous version") {
		t.Fatal(out)
	}
	h.call("edit_doc", map[string]any{"doc": ref(id), "append": "Signed in two copies."})
	if !strings.Contains(h.text(id), "Signed in two copies.") {
		t.Fatal("append failed")
	}

	// Refusals change nothing.
	before := readFile(t, path)
	for args, want := range map[string]string{
		`diff`:      "diff is not available for Word files",
		`missing`:   "was not found",
		`multirow`:  "new tables",
		`newline`:   "single line",
		`tracked`:   "tracked changes",
		`notatable`: "not in a table",
	} {
		a := map[string]any{"doc": ref(id)}
		switch args {
		case "diff":
			a["diff"] = "@@ -1 +1 @@\n-a\n+b\n"
		case "missing":
			a["old_string"], a["new_string"] = "nonexistent words", "x"
		case "multirow":
			a["insert"], a["after"] = "| a | b |\n|---|---|\n| 1 | 2 |", "Service terms"
		case "newline":
			a["old_string"], a["new_string"] = "Service terms", "a\nb"
		case "notatable":
			a["insert"], a["after"] = "| a | b |", "Service terms"
		case "tracked":
			id2 := h.upload("tracked.docx", tinyDocx(t, `<w:p><w:ins w:id="1" w:author="X"><w:r><w:t>Draft words</w:t></w:r></w:ins></w:p>`))
			a = map[string]any{"doc": ref(id2), "append": "x"}
		}
		if out := h.call("edit_doc", a); !strings.HasPrefix(out, "error:") || !strings.Contains(out, want) {
			t.Errorf("%s: %s", args, out)
		}
	}
	if !bytes.Equal(before, readFile(t, path)) {
		t.Fatal("a refused edit changed the file")
	}
}

func TestEditUploadedWordViaAppendSource(t *testing.T) {
	h := newGenHarness(t)
	id := h.upload("a.docx", tinyDocx(t, `<w:p><w:r><w:t>Alpha.</w:t></w:r></w:p>`))
	other := h.upload("b.docx", tinyDocx(t, `<w:p><w:r><w:t>Beta words.</w:t></w:r></w:p>`))
	out := h.call("edit_doc", map[string]any{"doc": ref(id), "append_source": []any{ref(other)}})
	if !strings.HasPrefix(out, "Edited Word document") || !strings.Contains(h.text(id), "Beta words.") {
		t.Fatalf("%s / %s", out, h.text(id))
	}
}

// A real LibreOffice-made .docx, edited in place and read back.
func TestEditLibreOfficeDocx(t *testing.T) {
	h := newGenHarness(t)
	var err error
	if h.a.Word, err = mdword.New(""); err != nil {
		t.Skip(err)
	}
	genID, genPath := h.create("create_word", "Made", "# Contract\n\nPayment is due in **30** days.\n\n| Item | Price |\n|---|---|\n| Rent | 500 |\n\nThe end.\n")
	data := readFile(t, genPath)
	id := h.upload("uploaded.docx", data) // same bytes, but no sidecar: edited in place
	if _, err := os.Stat(h.stored(id) + sidecarExt); err == nil {
		t.Fatal("unexpected sidecar")
	}
	_ = genID
	for _, args := range []map[string]any{
		{"old_string": "due in 30 days", "new_string": "due in 45 days"},
		{"insert": "Interest applies after that.", "after": "due in 45 days"},
		{"insert": "| Deposit | 900 |", "after": "| Rent | 500 |"},
		{"insert": "Last words.", "after": "The end."},
		{"append": "Appended."},
	} {
		args["doc"] = ref(id)
		if out := h.call("edit_doc", args); !strings.HasPrefix(out, "Edited Word document") {
			t.Fatalf("%v: %s", args, out)
		}
	}
	text, err := docxextract.Text(readFile(t, h.stored(id)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"due in 45 days", "Interest applies after that.", "Deposit", "900", "Last words.", "Appended."} {
		if !strings.Contains(text, want) || !strings.Contains(h.text(id), want) {
			t.Errorf("missing %q in file or index:\n%s", want, text)
		}
	}
	for _, f := range []string{"word/styles.xml", "word/settings.xml"} {
		if zipEntry(t, readFile(t, h.stored(id)), f) != zipEntry(t, data, f) {
			t.Errorf("%s changed", f)
		}
	}
}
