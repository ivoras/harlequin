package agent

import "context"

const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

const createWordPrompt = `Create a Word document (.docx) and save it as a file the user can download and edit in Word — for a report, letter, contract draft, summary or table the user wants "as a Word document". Normally you write ordinary Markdown (headings, paragraphs, **bold**, lists, pipe tables, > quotes, code blocks); a line containing only \newpage starts a new page. Use format "html" only when the user needs a layout Markdown cannot express (colours, spacing, alignment): then you write HTML with your own simple CSS in a <style> element; scripts, forms, and every external image, font or stylesheet are removed (only inline data: images stay). Give the text in ONE of three ways: content (the text itself, for up to about two pages), or source naming a sandbox file ("tmp://report.md", "storage://report.html" — build long documents in a file with edit_doc append), or source naming a saved text document ("u.12") to turn that document into a Word file. Set landscape to true for wide tables. load_skill "create-word" for the details. To change an existing Word document (one you made or one the user uploaded) use edit_doc.`

func (a *Agent) createWordEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_word", createWordPrompt+createDestHelp(wordSpec), createSchema(wordSpec,
			`File name for the document, e.g. "Q1 report" (".docx" is added). It is not printed: start the Markdown with a "# Heading" for the visible title.`,
			"The text itself (Markdown, or HTML with format \"html\").")),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.createFile(ctx, rc, args, wordSpec)
		},
	}
}
