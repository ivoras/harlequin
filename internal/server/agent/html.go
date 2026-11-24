package agent

import "context"

const createHTMLPrompt = `Create a self-contained HTML page (.html) and save it as a file the user can download and open in any browser or send on — for a report, summary, table, page of notes or anything the user wants "as an HTML file/page". Normally you write ordinary Markdown (headings, paragraphs, **bold**, lists, pipe tables, > quotes, code blocks) and the server wraps it in a clean, readable page. Use format "html" only when the user needs a layout Markdown cannot express (colours, columns, precise spacing): then you write HTML with your own CSS in a <style> element; scripts, forms, and every external image, font or stylesheet are removed (only inline data: images stay), so the page works offline and cannot run code. Give the text in ONE of three ways: content (the text itself, for up to about two pages), or source naming a sandbox file ("tmp://report.md", "storage://report.html" — build long documents in a file with edit_doc append), or source naming a saved text document ("u.12") to turn that document into a page. This is the same text-to-file pipeline as create_pdf and create_word, without page breaks or paper size. load_skill "create-html" for the details.`

func (a *Agent) createHTMLEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_html", createHTMLPrompt+createDestHelp(htmlSpec), createSchema(htmlSpec,
			`File name for the page, e.g. "Q1 report" (".html" is added). It is the page's title: start the Markdown with a "# Heading" for the visible one.`,
			"The text itself (Markdown, or HTML with format \"html\").")),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.createFile(ctx, rc, args, htmlSpec)
		},
	}
}
