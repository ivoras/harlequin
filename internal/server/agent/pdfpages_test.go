package agent

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/storage"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// pagesPDF builds a minimal PDF with one page per text.
func pagesPDF(texts ...string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	kids := ""
	for i := range texts {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(texts)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, t := range texts {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 400 200] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		s := fmt.Sprintf("BT /F1 14 Tf 20 100 Td (%s) Tj ET", t)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

// The payslip job end to end: look up many names in a PDF in one call, cut one
// file per person (and a merged one) in one call, and attach a result to mail.
func TestPDFPagesLookupAndAttach(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr, err := storage.New(dir, filepath.Join(dir, "harlequin.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	ex, err := pdfextract.New()
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if err := mgr.WithUser(ctx, 1, func(db *sql.DB) error {
		store := documents.NewStore(mgr.Shared, crossCheckFakeEmbedder{dim: 4})
		a := &Agent{DataDir: dir, Docs: store, Storage: mgr, PDFPages: ex}
		rc := &runContext{userDB: db, userID: 1}
		tools := a.buildTools(ctx, rc)
		call := func(tool string, args map[string]any) string {
			t.Helper()
			out, err := tools[tool].handler(ctx, rc, args)
			if err != nil {
				t.Fatalf("%s: %v", tool, err)
			}
			return out
		}
		upload := func(title string, pdf []byte) string {
			t.Helper()
			doc, err := store.IngestInto(ctx, db, types.CreateDocumentRequest{Title: title, Mime: pdfMime, Content: "indexed text of " + title}, 1)
			if err != nil {
				t.Fatal(err)
			}
			a.storeDocFile(ctx, db, documents.ScopePersonal, rc, doc, pdf, ".pdf")
			return fmt.Sprintf("u.%d", doc.ID)
		}
		slips := upload("plate.pdf", pagesPDF("Cover", "Platna lista Ana Horvat", "Platna lista KOVAC Ivan", "Prilog", "Ana   Horvat nastavak", "Kraj"))
		annex := upload("prilog.pdf", pagesPDF("Annex one", "Annex two"))
		call("save_doc", map[string]any{"title": "Biljeska", "content": "Samo tekst.", "is_raw": true})

		// Many phrases, one call: exact pages, a reordered name flagged as such,
		// and a clear "not found".
		out := call("check_text", map[string]any{"doc": slips, "texts": []any{"ana horvat", "Ivan Kovac", "Pero Peric", "Platna lista"}})
		for _, want := range []string{
			`Looked up 4 text(s) in "plate.pdf" (` + slips + `): 2 found, 1 found only as separate words, 1 not found`,
			`"ana horvat": page 2, 5`, `"Ivan Kovac": not as an exact phrase, but all its words are on page 3`,
			`"Pero Peric": NOT FOUND`, `"Platna lista": page 2, 3`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("lookup lacks %q in:\n%s", want, out)
			}
		}
		if out := call("check_text", map[string]any{"doc": slips, "text": "indexed text"}); !strings.Contains(out, "Found 1 occurrence") {
			t.Errorf("single-phrase check_text changed: %q", out)
		}

		// One call, several files: per person, plus a merge across two PDFs.
		out = call("pdf_pages", map[string]any{"doc": slips, "outputs": []any{
			map[string]any{"title": "Ana Horvat", "pages": "2,5"},
			map[string]any{"title": "Ivan Kovac.pdf", "pages": "3"},
			map[string]any{"title": "Spojeno", "parts": []any{
				map[string]any{"doc": slips, "pages": "6, 1"},
				map[string]any{"doc": annex},
			}},
		}})
		if !strings.Contains(out, "Created 3 PDF file(s)") || !strings.Contains(out, `"Ana Horvat.pdf" (2 page(s))`) || !strings.Contains(out, `"Spojeno.pdf" (4 page(s))`) {
			t.Fatalf("pdf_pages: %q", out)
		}
		files, _ := mgr.UserFilesDir(1)
		pagesOf := func(title string) (int64, string) {
			t.Helper()
			var id int64
			var mime string
			if err := db.QueryRow(`SELECT id, mime FROM documents WHERE title = ?`, title).Scan(&id, &mime); err != nil || mime != pdfMime {
				t.Fatalf("document %q: mime %q, %v", title, mime, err)
			}
			name, _, _, _ := store.StoredFile(ctx, db, id)
			data, err := os.ReadFile(filepath.Join(files, name))
			if err != nil {
				t.Fatal(err)
			}
			pages, err := ex.Pages(data)
			if err != nil {
				t.Fatal(err)
			}
			for i := range pages {
				pages[i] = strings.Join(strings.Fields(pages[i]), " ")
			}
			return id, strings.Join(pages, "|")
		}
		anaID, got := pagesOf("Ana Horvat.pdf")
		if got != "Platna lista Ana Horvat|Ana Horvat nastavak" {
			t.Errorf("Ana's file: %q", got)
		}
		if _, got := pagesOf("Ivan Kovac.pdf"); got != "Platna lista KOVAC Ivan" {
			t.Errorf("Ivan's file: %q", got)
		}
		if _, got := pagesOf("Spojeno.pdf"); got != "Kraj|Cover|Annex one|Annex two" {
			t.Errorf("merged file: %q", got)
		}
		// The new file is searchable by its own text, and the source is intact.
		if text, _ := store.FullText(ctx, db, anaID); !strings.Contains(text, "Platna lista Ana Horvat") {
			t.Errorf("index of the new file: %q", text)
		}
		if out := call("check_text", map[string]any{"doc": slips, "texts": []any{"Kraj"}}); !strings.Contains(out, `"Kraj": page 6`) {
			t.Errorf("source after pdf_pages: %q", out)
		}

		// Nothing is created when any output is wrong.
		var before int
		_ = db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&before)
		var note int64
		_ = db.QueryRow(`SELECT id FROM documents WHERE title = 'Biljeska'`).Scan(&note)
		for name, c := range map[string]struct {
			args map[string]any
			want string
		}{
			"missing page": {map[string]any{"doc": slips, "outputs": []any{map[string]any{"title": "ok", "pages": "1"}, map[string]any{"title": "bad", "pages": "5-9"}}}, "page 9 does not exist (the document has 6 pages)"},
			"bad spec":     {map[string]any{"doc": slips, "outputs": []any{map[string]any{"title": "x", "pages": "first"}}}, "cannot read pages"},
			"reversed":     {map[string]any{"doc": slips, "outputs": []any{map[string]any{"title": "x", "pages": "5-3"}}}, "bad page range"},
			"not a pdf":    {map[string]any{"doc": fmt.Sprintf("u.%d", note), "outputs": []any{map[string]any{"title": "x", "pages": "1"}}}, "is not a PDF"},
			"no doc":       {map[string]any{"outputs": []any{map[string]any{"title": "x", "pages": "1"}}}, "does not say which PDF"},
			"same title":   {map[string]any{"doc": slips, "outputs": []any{map[string]any{"title": "x", "pages": "1"}, map[string]any{"title": "X.pdf", "pages": "2"}}}, "both titled"},
			"no title":     {map[string]any{"doc": slips, "outputs": []any{map[string]any{"pages": "1"}}}, "has no title"},
			"no outputs":   {map[string]any{"doc": slips}, "outputs is required"},
			"unknown doc":  {map[string]any{"doc": "u.9999", "outputs": []any{map[string]any{"title": "x"}}}, "not found"},
		} {
			if out := call("pdf_pages", c.args); !strings.HasPrefix(out, "error:") || !strings.Contains(out, c.want) {
				t.Errorf("%s: got %q, want %q", name, out, c.want)
			}
		}
		var after int
		_ = db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&after)
		if after != before {
			t.Errorf("failed calls created %d document(s)", after-before)
		}

		// Attachments: a PDF goes out as the PDF, a text document as its text
		// file, a sandbox file as it is.
		at, errMsg := a.emailAttachment(ctx, rc, fmt.Sprintf("u.%d", anaID))
		if errMsg != "" || at.Name != "Ana Horvat.pdf" || at.Mime != pdfMime || !bytes.HasPrefix(at.Data, []byte("%PDF-")) {
			t.Errorf("PDF attachment: %q %q %d bytes (%s)", at.Name, at.Mime, len(at.Data), errMsg)
		}
		if at, errMsg := a.emailAttachment(ctx, rc, fmt.Sprintf("u.%d", note)); errMsg != "" || at.Name != "Biljeska.md" || string(at.Data) != "Samo tekst." {
			t.Errorf("text attachment: %q %q (%s)", at.Name, at.Data, errMsg)
		}
		_ = a.tmpRoot(1).Write("tablica.csv", []byte("a;b\n1;2\n"))
		if at, errMsg := a.emailAttachment(ctx, rc, "tmp://tablica.csv"); errMsg != "" || at.Name != "tablica.csv" || !strings.HasPrefix(at.Mime, "text/csv") || string(at.Data) != "a;b\n1;2\n" {
			t.Errorf("file attachment: %q %q (%s)", at.Name, at.Mime, errMsg)
		}
		if _, errMsg := a.emailAttachment(ctx, rc, "u.9999"); !strings.Contains(errMsg, "not found") {
			t.Errorf("missing attachment: %q", errMsg)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParsePageSpec(t *testing.T) {
	for spec, want := range map[string]string{"": "[1 2 3 4]", "ALL": "[1 2 3 4]", "3": "[3]", "4, 1-2;3": "[4 1 2 3]", " 2 - 3 ": "", "2-2": "[2]"} {
		got, err := parsePageSpec(spec, 4)
		if s := fmt.Sprint(got); (err == nil && s != want) || (err != nil && want != "") {
			t.Errorf("%q: %v %v, want %s", spec, got, err, want)
		}
	}
	for _, spec := range []string{"0", "5", "3-9", "a", "1-", "-2", ","} {
		if got, err := parsePageSpec(spec, 4); err == nil {
			t.Errorf("%q: expected an error, got %v", spec, got)
		}
	}
}
