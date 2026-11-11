package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const (
	maxPDFOutputs   = 200 // new PDFs per pdf_pages call
	maxLookupTexts  = 500 // phrases per check_text call
	maxLookupPages  = 40  // pages listed per phrase
	pdfMime         = "application/pdf"
	noPDFToolsError = "error: working with PDF pages is not available on this server (PDF support failed to start)."
)

// storedOriginal returns a document's stored original file: its bytes, the
// stored file name and its MIME type. ok is false when the document has none.
func (a *Agent) storedOriginal(ctx context.Context, rc *runContext, doc *docalign.Doc) (data []byte, name, mime string, ok bool) {
	db, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
	if errMsg != "" {
		return nil, "", "", false
	}
	stored, mime, _, err := a.Docs.StoredFile(ctx, db, doc.ID)
	dir := a.docFilesDir(rc, doc.Scope)
	if err != nil || stored == "" || dir == "" {
		return nil, "", "", false
	}
	if data, err = os.ReadFile(filepath.Join(dir, filepath.Base(stored))); err != nil {
		return nil, "", "", false
	}
	return data, filepath.Base(stored), mime, true
}

// pdfSource loads the PDF original of a document for the page tools.
func (a *Agent) pdfSource(ctx context.Context, rc *runContext, ref string) (*docalign.Doc, []byte, string) {
	doc, errMsg := a.loadDocRef(ctx, rc, ref)
	if errMsg != "" {
		return nil, nil, errMsg
	}
	data, _, mime, ok := a.storedOriginal(ctx, rc, doc)
	if !ok || (mime != pdfMime && !strings.HasPrefix(string(data), "%PDF-")) {
		return nil, nil, fmt.Sprintf("error: document %s (%q) is not a PDF with a stored file, so it has no pages to copy; pdf_pages works on PDF documents only (to make a PDF from text use create_pdf).", docRef(doc), doc.Title)
	}
	return doc, data, ""
}

// parsePageSpec reads "3,7,10-12" (1-based; "all" or "" = every page) into the
// pages it names, in the order written, checked against the page count.
func parsePageSpec(spec string, count int) ([]int, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" || spec == "all" {
		pages := make([]int, count)
		for i := range pages {
			pages[i] = i + 1
		}
		return pages, nil
	}
	var pages []int
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		lo, hi, isRange := strings.Cut(part, "-")
		from, err := strconv.Atoi(strings.TrimSpace(lo))
		to := from
		if err == nil && isRange {
			to, err = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err != nil {
			return nil, fmt.Errorf(`cannot read pages %q: write page numbers and ranges such as "3,7,10-12", or "all"`, part)
		}
		if from < 1 || to < from {
			return nil, fmt.Errorf("bad page range %q: pages start at 1 and a range goes from low to high", part)
		}
		if to > count {
			return nil, fmt.Errorf("page %d does not exist (the document has %d pages)", to, count)
		}
		for p := from; p <= to; p++ {
			pages = append(pages, p)
		}
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages given")
	}
	return pages, nil
}

func (a *Agent) pdfPagesEntry() toolEntry {
	part := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"doc":   map[string]any{"type": "string", "description": `PDF document to take pages from, e.g. "u.13"`},
			"pages": map[string]any{"type": "string", "description": `Pages to take, e.g. "1-2" (default: all)`},
		},
		"required": []string{"doc"},
	}
	return toolEntry{
		def: fnTool("pdf_pages", `Make new PDF files out of pages of existing PDF documents — extract pages, split a PDF into several files, merge PDFs, reorder or drop pages. Pages are copied exactly as they are (layout, fonts and images are kept); nothing is re-typed. Each entry of outputs becomes one new PDF document: give it a title and the pages to put in it, in order, as a list such as "3,7,10-12" (page numbers start at 1; "all" = every page). Set doc to the PDF the pages come from; one call can make up to 200 files, so to split a PDF into one file per person or per chapter pass ALL the outputs in ONE call, never one call each. To merge, or to mix pages of several PDFs, give an output parts instead of pages: a list of {doc, pages} taken in order. Find the page numbers first with check_text (its texts list looks up many names or phrases at once and returns their pages). The new PDFs are saved in the project in a project session, else in the user's personal files (scope "shared" is owner/admin only); the source documents are not changed. Only PDF documents have pages: to make a PDF from text use create_pdf.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc": map[string]any{"type": "string", "description": `The PDF document the pages come from, e.g. "u.12" (used by every output that gives pages)`},
				"outputs": map[string]any{"type": "array", "description": "The PDF files to create (1 to 200)", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string", "description": `File name of the new PDF, e.g. "Payslip Ana Horvat" (".pdf" is added)`},
						"pages": map[string]any{"type": "string", "description": `Pages of doc to copy, in order, e.g. "3,7,10-12" or "all"`},
						"parts": map[string]any{"type": "array", "items": part, "description": "Instead of pages: pages of several PDFs, in order (to merge)"},
					},
					"required": []string{"title"},
				}},
				"scope": map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
			},
			"required": []string{"outputs"},
		}),
		handler: a.pdfPages,
	}
}

func (a *Agent) pdfPages(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.PDFPages == nil {
		return noPDFToolsError, nil
	}
	outsArg, _ := args["outputs"].([]any)
	if len(outsArg) == 0 {
		return `error: outputs is required: a list of the PDFs to create, e.g. {"doc": "u.12", "outputs": [{"title": "Pages 3-5", "pages": "3-5"}]}`, nil
	}
	if len(outsArg) > maxPDFOutputs {
		return fmt.Sprintf("error: %d outputs in one call (limit %d); split them over several calls", len(outsArg), maxPDFOutputs), nil
	}
	scope, db, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}

	// Resolve everything before producing anything: a bad title or page number
	// in output 57 must not leave 56 files behind.
	type want struct{ doc, pages string }
	var sources [][]byte
	var srcDocs []*docalign.Doc
	index := map[string]int{}
	source := func(ref string) (int, string) {
		ref = attachedRef(ref)
		if i, ok := index[ref]; ok {
			return i, ""
		}
		doc, data, errMsg := a.pdfSource(ctx, rc, ref)
		if errMsg != "" {
			return 0, errMsg
		}
		sources, srcDocs = append(sources, data), append(srcDocs, doc)
		index[ref] = len(sources) - 1
		return index[ref], ""
	}
	defDoc := strings.TrimSpace(argString(args, "doc"))
	titles := make([]string, len(outsArg))
	wants := make([][]want, len(outsArg))
	seen := map[string]bool{}
	for i, o := range outsArg {
		m, _ := o.(map[string]any)
		title := strings.TrimSpace(argString(m, "title"))
		if title == "" {
			return fmt.Sprintf("error: output %d has no title", i+1), nil
		}
		if !strings.HasSuffix(strings.ToLower(title), ".pdf") {
			title += ".pdf"
		}
		if seen[strings.ToLower(title)] {
			return fmt.Sprintf("error: two outputs are both titled %q; give each file its own title", title), nil
		}
		seen[strings.ToLower(title)] = true
		titles[i] = title
		if parts, _ := m["parts"].([]any); len(parts) > 0 {
			if strings.TrimSpace(argString(m, "pages")) != "" {
				return fmt.Sprintf("error: output %d (%q) has both pages and parts; give one", i+1, title), nil
			}
			for _, p := range parts {
				pm, _ := p.(map[string]any)
				d := strings.TrimSpace(argString(pm, "doc"))
				if d == "" {
					d = defDoc
				}
				wants[i] = append(wants[i], want{d, argString(pm, "pages")})
			}
		} else {
			wants[i] = []want{{defDoc, argString(m, "pages")}}
		}
		for _, w := range wants[i] {
			if w.doc == "" {
				return fmt.Sprintf(`error: output %d (%q) does not say which PDF its pages come from; set doc, e.g. {"doc": "u.12", "outputs": [...]}`, i+1, title), nil
			}
			if _, errMsg := source(w.doc); errMsg != "" {
				return errMsg, nil
			}
		}
	}
	counts, err := a.PDFPages.PageCounts(sources)
	if err != nil {
		return "error: cannot read a source PDF: " + err.Error(), nil
	}
	plan := make([][]pdfextract.PagePart, len(wants))
	for i, ws := range wants {
		for _, w := range ws {
			si, _ := source(w.doc)
			pages, err := parsePageSpec(w.pages, counts[si])
			if err != nil {
				return fmt.Sprintf("error: output %d (%q), document %s: %v. Nothing was created.", i+1, titles[i], docRef(srcDocs[si]), err), nil
			}
			plan[i] = append(plan[i], pdfextract.PagePart{Source: si, Pages: pages})
		}
	}
	pdfs, err := a.PDFPages.Assemble(sources, plan)
	if err != nil {
		return "error: could not build the PDF: " + err.Error(), nil
	}

	var sb strings.Builder
	for i, pdf := range pdfs {
		// Index the new file's own page text (as an upload would), so it is
		// searchable and its citations carry page numbers.
		pages, _ := a.PDFPages.Pages(pdf)
		var text strings.Builder
		var starts []int
		off := 0
		for j, pg := range pages {
			if j > 0 {
				text.WriteString("\n\n")
				off += 2
			}
			starts = append(starts, off)
			text.WriteString(pg)
			off += utf8.RuneCountInString(pg)
		}
		content := text.String()
		if strings.TrimSpace(content) == "" { // scanned pages: nothing to index
			content, starts = strings.TrimSuffix(titles[i], ".pdf")+" (pages without extractable text)", nil
		}
		doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
			Title: titles[i], URI: "agent://pdf_pages", Mime: pdfMime, Content: content, PageStarts: starts, OriginalName: titles[i],
		}, rc.userID)
		if err != nil {
			return "", fmt.Errorf("saving %q (%d of %d files were created before it): %w", titles[i], i, len(pdfs), err)
		}
		a.storeDocFile(ctx, db, scope, rc, doc, pdf, ".pdf")
		a.publishDocChange(docevents.Created, scope, rc, doc.ID)
		fmt.Fprintf(&sb, "- %s.%d %q (%d page(s))\n", scopeLetter(scope), doc.ID, titles[i], len(pages))
	}
	return fmt.Sprintf("Created %d PDF file(s); the user can open or download them from their files:\n%s", len(pdfs), sb.String()), nil
}

// foldText prepares text for phrase matching: lower case, runs of whitespace
// (PDF text breaks lines anywhere) as one space.
func foldText(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// lookupTexts is check_text for a list of phrases: where each one occurs, as a
// table the page tools can act on. Units are pages for a PDF (read from the
// file itself, so they are exact), else the indexed pages, else lines.
func (a *Agent) lookupTexts(ctx context.Context, rc *runContext, ref string, texts []string) (string, error) {
	if len(texts) > maxLookupTexts {
		return fmt.Sprintf("error: %d texts in one call (limit %d); split them over several calls", len(texts), maxLookupTexts), nil
	}
	var units []string // folded text per unit
	var nums []int     // the number reported for each unit
	unit, name, note := "page", "", ""
	if isSandboxPath(ref) {
		body, label, errMsg := a.sourceText(ctx, rc, ref)
		if errMsg != "" {
			return errMsg, nil
		}
		unit, name = "line", label
		for i, line := range strings.Split(body, "\n") {
			units, nums = append(units, foldText(line)), append(nums, i+1)
		}
	} else {
		doc, errMsg := a.loadDocRef(ctx, rc, ref)
		if errMsg != "" {
			return errMsg, nil
		}
		name = fmt.Sprintf("%q (%s)", doc.Title, docRef(doc))
		if data, _, mime, ok := a.storedOriginal(ctx, rc, doc); ok && mime == pdfMime && a.PDFPages != nil {
			if pages, err := a.PDFPages.Pages(data); err == nil && strings.TrimSpace(strings.Join(pages, "")) != "" {
				for i, pg := range pages {
					units, nums = append(units, foldText(pg)), append(nums, i+1)
				}
			}
		}
		if units == nil { // not a PDF with a text layer: the index, by section
			db, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
			if errMsg != "" {
				return errMsg, nil
			}
			rows, err := db.QueryContext(ctx, `SELECT page, ord, content FROM doc_chunks WHERE document_id = ? ORDER BY ord`, doc.ID)
			if err != nil {
				return "", err
			}
			defer rows.Close()
			paged := false
			for rows.Next() {
				var page, ord int
				var content string
				if err := rows.Scan(&page, &ord, &content); err != nil {
					return "", err
				}
				paged = paged || page > 0
				if page == 0 {
					page = ord + 1
				}
				units, nums = append(units, foldText(content)), append(nums, page)
			}
			if err := rows.Err(); err != nil {
				return "", err
			}
			if !paged {
				unit = "section"
			} else {
				note = " Page numbers here come from the search index and are where a passage starts; a match near a page end may be on the next page."
			}
		}
	}

	where := func(match func(string) bool) []int {
		var out []int
		for i, u := range units {
			if match(u) && (len(out) == 0 || out[len(out)-1] != nums[i]) {
				out = append(out, nums[i])
			}
		}
		sort.Ints(out)
		return out
	}
	list := func(ns []int) string {
		var parts []string
		for i, n := range ns {
			if i == maxLookupPages {
				parts = append(parts, fmt.Sprintf("… (%d in all)", len(ns)))
				break
			}
			parts = append(parts, strconv.Itoa(n))
		}
		return strings.Join(parts, ", ")
	}
	var sb strings.Builder
	found, loose := 0, 0
	var missing []string
	for _, t := range texts {
		f := foldText(t)
		if hits := where(func(u string) bool { return strings.Contains(u, f) }); len(hits) > 0 {
			found++
			fmt.Fprintf(&sb, "%q: %s %s\n", t, unit, list(hits))
			continue
		}
		// Names are often printed in another order ("HORVAT Ana"): the same
		// words on one page are worth reporting, marked as not an exact match.
		words := strings.Fields(f)
		if hits := where(func(u string) bool {
			for _, w := range words {
				if !strings.Contains(u, w) {
					return false
				}
			}
			return true
		}); len(words) > 1 && len(hits) > 0 {
			loose++
			fmt.Fprintf(&sb, "%q: not as an exact phrase, but all its words are on %s %s — check before relying on it\n", t, unit, list(hits))
			continue
		}
		missing = append(missing, t)
		fmt.Fprintf(&sb, "%q: NOT FOUND\n", t)
	}
	head := fmt.Sprintf("Looked up %d text(s) in %s: %d found", len(texts), name, found)
	if loose > 0 {
		head += fmt.Sprintf(", %d found only as separate words", loose)
	}
	if len(missing) > 0 {
		head += fmt.Sprintf(", %d not found — tell the user which", len(missing))
	}
	return head + "." + note + "\n" + sb.String(), nil
}
