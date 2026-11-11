package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/mdpdf"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const createPDFPrompt = `Create a PDF document and save it as a file the user can download — for a report, letter, summary, table or any text the user wants "as a PDF". Normally you write ordinary Markdown (headings, paragraphs, **bold**, lists, pipe tables, > quotes, code blocks) and the server typesets it on A4 pages with page numbers; a line containing only \newpage starts a new page. Use format "html" only when the user needs a layout Markdown cannot express (colours, columns, a letterhead, precise spacing): then you write HTML with your own CSS in a <style> element; scripts, forms, and every external image, font or stylesheet are removed (only inline data: images stay). Give the text in ONE of three ways: content (the text itself, for up to about two pages), or source naming a sandbox file ("tmp://report.md", "storage://report.html" — build long documents with edit_file append), or source naming a saved text document ("u.12") to turn that document into a PDF. Set landscape to true for wide tables. load_skill "create-pdf" for the details. The PDF lands in the project in a project session, else in the user's personal files; scope "shared" is owner/admin only. This tool writes NEW documents: to copy, extract, split or merge pages of existing PDFs use pdf_pages.`

func (a *Agent) createPDFEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_pdf", createPDFPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":     map[string]any{"type": "string", "description": `File name for the PDF, e.g. "Q1 report" (".pdf" is added). It is not printed: start the Markdown with a "# Heading" for the visible title.`},
				"content":   map[string]any{"type": "string", "description": "The text itself (Markdown, or HTML with format \"html\"). Omit when source is given."},
				"source":    map[string]any{"type": "string", "description": "Where to read the text from instead: a sandbox file (tmp://report.md, storage://report.html) or a saved text document (u.12). Omit when content is given."},
				"format":    map[string]any{"type": "string", "enum": []string{"markdown", "html"}, "description": "What the text is. Default markdown (html when source ends in .html or the text starts with <!DOCTYPE or <html)."},
				"landscape": map[string]any{"type": "boolean", "description": "Landscape A4 pages (for wide tables). Default false: portrait."},
				"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
			},
			"required": []string{"title"},
		}),
		handler: a.createPDF,
	}
}

func (a *Agent) createPDF(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.PDF == nil {
		// The tool is offered regardless, so the user hears why instead of "I can't".
		return "error: PDF creation is not set up on this server (an administrator has to install headless Chrome; see the README). Tell the user, and offer to save the text as a Markdown document with save_doc instead.", nil
	}
	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		return "error: title is required", nil
	}
	if !strings.HasSuffix(strings.ToLower(title), ".pdf") {
		title += ".pdf"
	}
	src, errMsg := a.markdownArg(ctx, rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	isHTML, errMsg := sourceIsHTML(args, src)
	if errMsg != "" {
		return errMsg, nil
	}
	landscape, _ := args["landscape"].(bool)
	page, text, st, err := mdpdf.Page(strings.TrimSuffix(title, ".pdf"), src, isHTML, landscape)
	if err != nil {
		return "error: " + err.Error() + " — pass the text as content, or as source (a sandbox file or a saved document)", nil
	}
	scope, db, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	pdf, err := a.PDF.PDF(ctx, page)
	if err != nil {
		return "error: could not create the PDF: " + err.Error(), nil
	}
	// The searchable text is the Markdown the PDF was typeset from (HTML input
	// converted to it), so search_docs and read_doc work on it at once.
	doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://create_pdf", Mime: "application/pdf", Content: text,
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeDocFile(ctx, db, scope, rc, doc, pdf, ".pdf")
	a.publishDocChange(docevents.Created, scope, rc, doc.ID)
	msg := fmt.Sprintf("Created PDF %s.%d %q", scopeLetter(scope), doc.ID, title)
	if n := mdpdf.Pages(pdf); n > 0 {
		msg += fmt.Sprintf(": %d page(s)", n)
	}
	msg += ". The user can open or download it from their files."
	if st.ImagesDropped > 0 {
		msg += fmt.Sprintf(" Note: %d image(s) were left out (only inline data: images can be printed); tell the user.", st.ImagesDropped)
	}
	if st.Removed > 0 {
		msg += fmt.Sprintf(" Note: %d script, form, embedded or external item(s) were removed from the HTML (a PDF is static and self-contained).", st.Removed)
	}
	return msg, nil
}

// sourceIsHTML decides whether a create_pdf/create_word text is HTML: the
// format argument, else a .html source name or a text that starts as an HTML
// document. Markdown that merely mentions tags stays Markdown.
func sourceIsHTML(args map[string]any, src string) (isHTML bool, errMsg string) {
	switch strings.ToLower(strings.TrimSpace(argString(args, "format"))) {
	case "html":
		return true, ""
	case "markdown", "md":
		return false, ""
	case "":
		name := strings.ToLower(strings.TrimSpace(argString(args, "source")))
		head := strings.ToLower(strings.TrimSpace(src))
		return strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm") ||
			strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html"), ""
	}
	return false, `error: format must be "markdown" or "html"`
}
