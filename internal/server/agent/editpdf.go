package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const maxPDFMarks = 200 // marks per edit_pdf call

// pdfIndexText joins page texts the way an upload does and records where each
// page starts, so the new file is searchable and its citations carry pages.
func pdfIndexText(title string, pages []string) (string, []int) {
	var text strings.Builder
	var starts []int
	off := 0
	for i, pg := range pages {
		if i > 0 {
			text.WriteString("\n\n")
			off += 2
		}
		starts = append(starts, off)
		text.WriteString(pg)
		off += utf8.RuneCountInString(pg)
	}
	if strings.TrimSpace(text.String()) == "" { // scanned pages: nothing to index
		return strings.TrimSuffix(title, ".pdf") + " (pages without extractable text)", nil
	}
	return text.String(), starts
}

// writeSandboxFile saves data at a sandbox path (tmp://, storage://, bare = tmp://).
// It returns the path as stored, or an error message for the model.
func (a *Agent) writeSandboxFile(rc *runContext, target string, data []byte, overwrite bool) (string, string) {
	root, prefix, rel, err := a.resolveNamespaceRoot(rc, strings.TrimSpace(target))
	if err != nil {
		return "", "error: " + err.Error()
	}
	if rel == "" {
		return "", "error: save_to must name a file, e.g. tmp://result.pdf"
	}
	if exists, _ := root.Exists(rel); exists && !overwrite {
		return "", fmt.Sprintf("error: %s already exists; choose another name, or pass overwrite: true to replace it", target)
	}
	if err := root.Write(rel, data); err != nil {
		return "", fmt.Sprintf("error: cannot write %s: %v", target, err)
	}
	return prefix + rel, ""
}

// pdfInput reads the PDF a tool works on: a saved PDF document (u.12) or a
// sandbox file (tmp://a.pdf). doc is nil for a sandbox file.
func (a *Agent) pdfInput(ctx context.Context, rc *runContext, ref string) (doc *docalign.Doc, data []byte, name, errMsg string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil, "", `error: doc is required: a PDF document ("u.12") or a sandbox PDF ("tmp://a.pdf")`
	}
	if isSandboxPath(ref) {
		root, _, rel, err := a.resolveNamespaceRoot(rc, ref)
		if err != nil {
			return nil, nil, "", "error: " + err.Error()
		}
		if data, err = root.Read(rel); err != nil {
			return nil, nil, "", fmt.Sprintf("error: cannot read %s: %v", ref, err)
		}
		if !strings.HasPrefix(string(data), "%PDF-") {
			return nil, nil, "", fmt.Sprintf("error: %s is not a PDF file", ref)
		}
		return nil, data, strings.TrimSuffix(rel[strings.LastIndex(rel, "/")+1:], ".pdf"), ""
	}
	doc, data, errMsg = a.pdfSource(ctx, rc, ref)
	if errMsg != "" {
		return nil, nil, "", strings.Replace(errMsg, "pdf_pages works on PDF documents only (to make a PDF from text use create_pdf)", "edit_pdf works on PDF files only", 1)
	}
	return doc, data, strings.TrimSuffix(doc.Title, ".pdf"), ""
}

const editPDFPrompt = `Annotate a PDF: highlight, underline, strike out or box passages, and attach comments or sticky notes. The marks are standard PDF annotations that every viewer shows, edits and prints; the pages and text are not changed. doc is a saved PDF document ("u.12") or a sandbox PDF ("tmp://a.pdf"). marks is a list; each mark has a kind (highlight, underline, strikeout, squiggly, box, note) and usually text, the exact phrase to mark (case-insensitive; EVERY occurrence is marked unless you give page and/or nth, so for one passage give page, or a longer phrase). Phrases are matched within a line: do not send text that spans a line break (mark the first words). Add comment to attach a comment to the mark (what the user reads when they hover or click it); color is yellow, green, blue, pink, red, orange, purple or #RRGGBB. A note (kind "note") with text puts a sticky-note icon at the phrase; without text, give page and x, y (points from the page's top-left corner, A4 is about 595 x 842). load_skill "edit-pdf" for how to choose phrases, kinds, colours and comments. Find the pages first with check_text, or read_doc, and call this tool ONCE with all the marks. The result lists, per mark, how many annotations were made and on which pages, and what could not be found: tell the user about those. Like edit_doc, it edits IN PLACE by default: the saved document keeps its id, title and scope and the user's file now shows the marks (edit_doc with doc and undo true restores the version before the last edit, one step back); a sandbox PDF is changed where it is. Give save_as, a title, to leave the original untouched and save the result as a NEW document instead (in the project in a project session, else personal, or scope "shared" for owner/admin); give save_to, a sandbox path such as "tmp://marked.pdf", to write a new temporary file (write its tmp:// path in your reply: the user gets a download link, valid for a while). A PDF made by create_pdf cannot be marked in place (it is rebuilt from its text whenever it is edited, which would drop the marks): use save_as.`

func (a *Agent) editPDFEntry() toolEntry {
	mark := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":    map[string]any{"type": "string", "enum": []string{"highlight", "underline", "strikeout", "squiggly", "box", "note"}},
			"text":    map[string]any{"type": "string", "description": "The phrase to mark, exactly as in the document (one line; case-insensitive). Omit only for a positioned note."},
			"page":    map[string]any{"type": "integer", "description": "Only this page (starts at 1). Default: every page where the phrase occurs. Required for a note without text."},
			"nth":     map[string]any{"type": "integer", "description": "Only the nth occurrence of the phrase (counting from the first page). Default: all."},
			"comment": map[string]any{"type": "string", "description": "A comment to attach to this mark."},
			"color":   map[string]any{"type": "string", "description": "yellow (default for highlight and note), green, blue (underline), pink, red (strikeout, squiggly, box), orange, purple, or #RRGGBB."},
			"x":       map[string]any{"type": "number", "description": "Note without text: distance in points from the left edge."},
			"y":       map[string]any{"type": "number", "description": "Note without text: distance in points from the top edge."},
		},
		"required": []string{"kind"},
	}
	return toolEntry{
		def: fnTool("edit_pdf", editPDFPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"doc":       map[string]any{"type": "string", "description": `The PDF to annotate: a document id ("u.12") or a sandbox file ("tmp://a.pdf")`},
				"marks":     map[string]any{"type": "array", "items": mark, "description": "The annotations to add (up to 200)."},
				"save_as":   map[string]any{"type": "string", "description": `Optional: leave the original unchanged and save the marked PDF as a NEW document with this title (".pdf" is added).`},
				"save_to":   map[string]any{"type": "string", "description": `Optional: leave the original unchanged and write the marked PDF to this sandbox file (e.g. "tmp://marked.pdf"). It is a temporary file: write its tmp:// path in your reply and the user gets a download link.`},
				"overwrite": map[string]any{"type": "boolean", "description": "With save_to: replace the file if it exists."},
				"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}, "description": "With save_as: where the new document goes. Default: the project in a project session, else personal."},
			},
			"required": []string{"doc", "marks"},
		}),
		handler: a.editPDF,
	}
}

// commitStoredFile replaces a stored file with newFile, keeping the old one as
// .prev (one step of undo). The new file is staged first, so a failure leaves
// the document as it was.
func commitStoredFile(path string, newFile []byte) error {
	if err := os.WriteFile(path+".tmp", newFile, 0o600); err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(path+prevExt, old, 0o600); err != nil {
			os.Remove(path + ".tmp")
			return err
		}
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		os.Remove(path + ".tmp")
		return err
	}
	return nil
}

// editableDoc loads a saved document for in-place editing and applies the
// permission rules edit_doc uses.
func (a *Agent) editableDoc(ctx context.Context, rc *runContext, ref string) (*docalign.Doc, *sql.DB, string) {
	doc, errMsg := a.loadDocRef(ctx, rc, ref)
	if errMsg != "" {
		return nil, nil, errMsg
	}
	switch doc.Scope {
	case documents.ScopeShared:
		if !rc.canShareMemory {
			return nil, nil, "error: only owner or admin users can edit shared documents; ask an owner/admin, or pass save_as to save a marked copy of your own."
		}
	case documents.ScopePersonal, documents.ScopeProject:
	default:
		return nil, nil, "error: this document belongs to another project's corpus and cannot be edited from this session; pass save_as to save a marked copy."
	}
	db, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
	if errMsg != "" {
		return nil, nil, errMsg
	}
	return doc, db, ""
}

func (a *Agent) editPDF(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.PDFPages == nil {
		return noPDFToolsError, nil
	}
	rawMarks, _ := args["marks"].([]any)
	if len(rawMarks) == 0 {
		return "error: marks is empty: give at least one mark, e.g. {\"kind\": \"highlight\", \"text\": \"total amount\"}", nil
	}
	if len(rawMarks) > maxPDFMarks {
		return fmt.Sprintf("error: %d marks is too many for one call (the limit is %d)", len(rawMarks), maxPDFMarks), nil
	}
	marks := make([]pdfextract.Mark, len(rawMarks))
	for i, r := range rawMarks {
		m, _ := r.(map[string]any)
		marks[i] = pdfextract.Mark{
			Kind: argString(m, "kind"), Text: argString(m, "text"), Comment: argString(m, "comment"),
			Color: argString(m, "color"), Author: rc.username,
		}
		marks[i].Page, marks[i].Nth = argInt(m, "page", 0), argInt(m, "nth", 0)
		marks[i].X, _ = m["x"].(float64)
		marks[i].Y, _ = m["y"].(float64)
	}

	// Where the result goes: save_to (a new sandbox file), save_as (a new
	// document), else in place — as edit_doc does.
	saveTo := strings.TrimSpace(argString(args, "save_to"))
	saveAs := strings.TrimSpace(argString(args, "save_as"))
	if saveTo != "" && saveAs != "" {
		return "error: give either save_as (a new document) or save_to (a new sandbox file), not both; give neither to edit the PDF in place", nil
	}
	if saveTo != "" && !isSandboxPath(saveTo) {
		return `error: save_to must be a sandbox file (tmp://marked.pdf or storage://marked.pdf). For a new document the user can download, use save_as with a title; to edit in place give neither.`, nil
	}
	var scope string
	if saveAs != "" {
		var errMsg string
		if scope, _, errMsg = a.saveScope(rc, args); errMsg != "" {
			return errMsg, nil
		}
	}

	ref := strings.TrimSpace(argString(args, "doc"))
	inPlace := saveTo == "" && saveAs == ""
	var (
		doc      *docalign.Doc
		docDB    *sql.DB
		path     string // the stored file, for a document edited in place
		data     []byte
		name     string
		errMsg   string
		sandbox  = isSandboxPath(ref)
		sandRoot *sandboxfs.Root
		sandRel  string
	)
	switch {
	case inPlace && !sandbox:
		if doc, docDB, errMsg = a.editableDoc(ctx, rc, ref); errMsg != "" {
			return errMsg, nil
		}
		stored, mime, _, err := a.Docs.StoredFile(ctx, docDB, doc.ID)
		dir := a.docFilesDir(rc, doc.Scope)
		if err != nil || stored == "" || dir == "" {
			return fmt.Sprintf("error: document %s has no stored PDF file to mark; pass save_as to make a marked copy.", docRef(doc)), nil
		}
		path = filepath.Join(dir, filepath.Base(stored))
		if data, err = os.ReadFile(path); err != nil || (mime != pdfMime && !strings.HasPrefix(string(data), "%PDF-")) {
			return fmt.Sprintf("error: document %s (%q) is not a PDF with a stored file; edit_pdf works on PDF files only.", docRef(doc), doc.Title), nil
		}
		if _, err := os.Stat(path + sidecarExt); err == nil {
			return fmt.Sprintf("error: document %s was made by create_pdf and is rebuilt from its text whenever it is edited, which would drop the marks. Pass save_as (a title) to save a marked copy as a new document.", docRef(doc)), nil
		}
		name = strings.TrimSuffix(doc.Title, ".pdf")
	case inPlace && sandbox:
		var err error
		if sandRoot, _, sandRel, err = a.resolveNamespaceRoot(rc, ref); err != nil {
			return "error: " + err.Error(), nil
		}
		if doc, data, name, errMsg = a.pdfInput(ctx, rc, ref); errMsg != "" {
			return errMsg, nil
		}
	default:
		if doc, data, name, errMsg = a.pdfInput(ctx, rc, ref); errMsg != "" {
			return errMsg, nil
		}
	}

	out, results, err := a.PDFPages.Annotate(data, marks)
	if err != nil {
		return "error: cannot annotate this PDF: " + err.Error(), nil
	}
	total := 0
	var sb strings.Builder
	for i, r := range results {
		total += r.Applied
		label := strings.TrimSpace(marks[i].Kind + " " + marks[i].Text)
		switch {
		case r.Applied > 0 && r.Problem == "":
			fmt.Fprintf(&sb, "- %s: %d on page(s) %s\n", label, r.Applied, intList(r.Pages))
		case r.Applied > 0:
			fmt.Fprintf(&sb, "- %s: %d on page(s) %s; %s\n", label, r.Applied, intList(r.Pages), r.Problem)
		default:
			fmt.Fprintf(&sb, "- %s: NOT done: %s\n", label, r.Problem)
		}
	}
	if total == 0 {
		return "error: nothing was marked, so nothing was changed:\n" + sb.String() + "Check the phrases with read_doc or check_text (they must match one line of text exactly, apart from case); scanned PDFs have no text to mark.", nil
	}

	switch {
	case saveTo != "":
		path, errMsg := a.writeSandboxFile(rc, saveTo, out, argBool(args, "overwrite", false))
		if errMsg != "" {
			return errMsg, nil
		}
		return fmt.Sprintf("Marked PDF written to the new sandbox file %s (%d annotation(s)); the original is unchanged. It is a temporary file: write the path %s in your reply and the user gets a download link (it is removed after a while). To keep it as a saved document call copy_doc with source %q (no target).\n%s", path, total, path, path, sb.String()), nil

	case inPlace && sandbox:
		if err := sandRoot.Write(sandRel, out); err != nil {
			return fmt.Sprintf("error: cannot write %s: %v", ref, err), nil
		}
		return fmt.Sprintf("Marked %s in place (%d annotation(s)).\n%s", ref, total, sb.String()), nil

	case inPlace:
		if err := commitStoredFile(path, out); err != nil {
			return fmt.Sprintf("error: cannot write the stored file (%v); nothing was changed.", err), nil
		}
		a.publishDocChange(docevents.Updated, doc.Scope, rc, doc.ID)
		return fmt.Sprintf("Marked document %s (%q) in place: %d annotation(s) added. The user's file now shows them (the text is unchanged, so search results are the same). edit_doc with doc and undo true restores the version before this edit.\n%s",
			docRef(doc), doc.Title, total, sb.String()), nil
	}

	title := saveAs
	if !strings.HasSuffix(strings.ToLower(title), ".pdf") {
		title += ".pdf"
	}
	pages, _ := a.PDFPages.Pages(out)
	content, starts := pdfIndexText(title, pages)
	scope, db, _ := a.saveScope(rc, args)
	nd, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://edit_pdf", Mime: pdfMime, Content: content, PageStarts: starts, OriginalName: title,
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeDocFile(ctx, db, scope, rc, nd, out, ".pdf")
	a.publishDocChange(docevents.Created, scope, rc, nd.ID)
	from := name
	if doc != nil {
		from = docRef(doc)
	}
	return fmt.Sprintf("Saved the marked PDF as a new document %s.%d %q (%d annotation(s), made from %s, which is unchanged). The user can open or download it from their files.\n%s",
		scopeLetter(scope), nd.ID, title, total, from, sb.String()), nil
}

func intList(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = fmt.Sprint(n)
	}
	return strings.Join(s, ", ")
}
