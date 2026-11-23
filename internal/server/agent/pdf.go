package agent

import (
	"context"
	"strings"
)

const createPDFPrompt = `Create a PDF document and save it as a file the user can download — for a report, letter, summary, table or any text the user wants "as a PDF". Normally you write ordinary Markdown (headings, paragraphs, **bold**, lists, pipe tables, > quotes, code blocks) and the server typesets it on A4 pages with page numbers; a line containing only \newpage starts a new page. Use format "html" only when the user needs a layout Markdown cannot express (colours, columns, a letterhead, precise spacing): then you write HTML with your own CSS in a <style> element; scripts, forms, and every external image, font or stylesheet are removed (only inline data: images stay). Give the text in ONE of three ways: content (the text itself, for up to about two pages), or source naming a sandbox file ("tmp://report.md", "storage://report.html" — build long documents with edit_file append), or source naming a saved text document ("u.12") to turn that document into a PDF. Set landscape to true for wide tables. load_skill "create-pdf" for the details. To copy, extract, split or merge pages of existing PDFs use pdf_pages.`

func (a *Agent) createPDFEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_pdf", createPDFPrompt+createDestHelp(pdfSpec), createSchema(pdfSpec,
			`File name for the PDF, e.g. "Q1 report" (".pdf" is added). It is not printed: start the Markdown with a "# Heading" for the visible title.`,
			"The text itself (Markdown, or HTML with format \"html\").")),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.createFile(ctx, rc, args, pdfSpec)
		},
	}
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
