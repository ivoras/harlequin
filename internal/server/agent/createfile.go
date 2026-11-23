package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// createSpec describes one create_* tool. They share one pipeline (createFile)
// and one parameter set, so a parameter means the same in all of them: title,
// content/source (+ format where the tool takes HTML), landscape where pages
// exist, scope, and save_to/overwrite for a temporary file.
type createSpec struct {
	tool      string // create_pdf
	label     string // "PDF", used in the result message
	ext       string // ".pdf"
	mime      string
	kind      string // genPDF, ... (the sidecar kind)
	html      bool   // accepts format "html" (else Markdown only)
	landscape bool   // takes landscape (paged output)
	example   string // example file name for prompts, e.g. "report.pdf"
	hint      string // appended to a conversion error
}

var (
	pdfSpec = createSpec{tool: "create_pdf", label: "PDF", ext: ".pdf", mime: "application/pdf", kind: genPDF, html: true, landscape: true, example: "report.pdf",
		hint: " — pass the text as content, or as source (a sandbox file or a saved document)"}
	wordSpec = createSpec{tool: "create_word", label: "Word document", ext: ".docx", mime: docxMime, kind: genWord, html: true, landscape: true, example: "report.docx",
		hint: " — pass the text as content, or as source (a sandbox file or a saved document)"}
	excelSpec = createSpec{tool: "create_excel", label: "Excel workbook", ext: ".xlsx", mime: xlsxMime, kind: genExcel, example: "sales.xlsx",
		hint: ` — each table row must be a line starting with "|"; see load_skill "create-excel"`}
	htmlSpec = createSpec{tool: "create_html", label: "HTML page", ext: ".html", mime: "text/html", kind: genHTML, html: true, example: "report.html",
		hint: " — pass the text as content, or as source (a sandbox file or a saved document)"}
)

// createDestHelp is the "where does it go" paragraph every create_* prompt ends with.
func createDestHelp(sp createSpec) string {
	return fmt.Sprintf(` Where it goes: a new saved document the user can open or download from their files — in the project in a project session, else in the user's personal files (scope "shared" is owner/admin only). Give save_to, a sandbox path such as "tmp://%s", to write a temporary file instead: write that path in your reply and the user gets a download link (it is removed after a while); use it for a working file for another tool, or when the user only wants to download it. %s only makes NEW files: to change an existing one use edit_doc.`, sp.example, sp.tool)
}

// createSchema is the shared parameter set; titleDesc and contentDesc are the
// only tool-specific texts.
func createSchema(sp createSpec, titleDesc, contentDesc string) map[string]any {
	props := map[string]any{
		"title":   map[string]any{"type": "string", "description": titleDesc},
		"content": map[string]any{"type": "string", "description": contentDesc + " Omit when source is given."},
		"source": map[string]any{"type": "string", "description": "Where to read the text from instead: a sandbox file (tmp://" + strings.TrimSuffix(sp.example, sp.ext) +
			".md, storage://notes.md) or a saved text document (u.12). Omit when content is given."},
		"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}, "description": "Where the new document goes. Default: the project in a project session, else personal."},
		"save_to":   map[string]any{"type": "string", "description": `Optional: write the file to this sandbox path (e.g. "tmp://` + sp.example + `") instead of saving a document. It is a temporary file: write its tmp:// path in your reply and the user gets a download link.`},
		"overwrite": map[string]any{"type": "boolean", "description": "With save_to: replace the file if it exists."},
	}
	if sp.html {
		props["format"] = map[string]any{"type": "string", "enum": []string{"markdown", "html"}, "description": "What the text is. Default markdown (html when source ends in .html or the text starts with <!DOCTYPE or <html)."}
	}
	if sp.landscape {
		props["landscape"] = map[string]any{"type": "boolean", "description": "Landscape A4 pages (for wide tables). Default false: portrait."}
	}
	return map[string]any{"type": "object", "properties": props, "required": []string{"title"}}
}

// createFile is the one pipeline behind every create_* tool: read the text,
// generate the file, then either write a temporary sandbox file (save_to) or
// save a document (index, store with its source sidecar, announce).
func (a *Agent) createFile(ctx context.Context, rc *runContext, args map[string]any, sp createSpec) (string, error) {
	if msg := a.notSetUp(sp.kind); msg != "" {
		// The tool is offered regardless, so the user hears why instead of "I can't".
		return msg, nil
	}
	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		return "error: title is required", nil
	}
	if !strings.HasSuffix(strings.ToLower(title), sp.ext) {
		title += sp.ext
	}
	src, errMsg := a.markdownArg(ctx, rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	isHTML := false
	if sp.html {
		if isHTML, errMsg = sourceIsHTML(args, src); errMsg != "" {
			return errMsg, nil
		}
	}
	landscape := sp.landscape && argBool(args, "landscape", false)
	saveTo := strings.TrimSpace(argString(args, "save_to"))
	if saveTo != "" && !isSandboxPath(saveTo) {
		return `error: save_to must be a sandbox file (tmp://` + sp.example + ` or storage://` + sp.example + `). To save a document the user can keep, omit save_to.`, nil
	}
	scope, db, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	gs := genSource{Kind: sp.kind, Format: formatName(isHTML), Landscape: landscape, Source: src}
	g, err := a.generate(ctx, title, gs, true)
	if err != nil {
		if _, conv := err.(convertError); conv {
			return "error: " + err.Error() + sp.hint, nil
		}
		return "error: " + err.Error(), nil
	}
	if saveTo != "" {
		path, errMsg := a.writeSandboxFile(rc, saveTo, g.data, argBool(args, "overwrite", false))
		if errMsg != "" {
			return errMsg, nil
		}
		return fmt.Sprintf("%s written to %s%s. It is a temporary file: write the path %s in your reply and the user gets a download link (it is removed after a while). To keep it as a saved document call copy_doc with source %q (no target).%s",
			sp.label, path, g.detail, path, path, g.notes), nil
	}
	// The searchable text is what the file was made from (HTML input converted
	// to Markdown), so search_docs and read_doc work on it at once.
	doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://" + sp.tool, Mime: sp.mime, Content: g.text,
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeGenerated(ctx, db, scope, rc, doc, g.data, sp.ext, gs)
	a.publishDocChange(docevents.Created, scope, rc, doc.ID)
	return fmt.Sprintf("Created %s %s.%d %q%s. The user can open or download it from their files.%s", sp.label, scopeLetter(scope), doc.ID, title, g.detail, g.notes), nil
}
