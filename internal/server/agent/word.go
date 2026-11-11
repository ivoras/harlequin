package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

const createWordPrompt = `Create a Word document (.docx) and save it as a file the user can download and edit in Word — for a report, letter, contract draft, summary or table the user wants "as a Word document". Normally you write ordinary Markdown (headings, paragraphs, **bold**, lists, pipe tables, > quotes, code blocks); a line containing only \newpage starts a new page. Use format "html" only when the user needs a layout Markdown cannot express (colours, spacing, alignment): then you write HTML with your own simple CSS in a <style> element; scripts, forms, and every external image, font or stylesheet are removed (only inline data: images stay). Give the text in ONE of three ways: content (the text itself, for up to about two pages), or source naming a sandbox file ("tmp://report.md", "storage://report.html" — build long documents with edit_file append), or source naming a saved text document ("u.12") to turn that document into a Word file. Set landscape to true for wide tables. load_skill "create-word" for the details. The file lands in the project in a project session, else in the user's personal files; scope "shared" is owner/admin only. This tool writes NEW documents: it cannot edit an existing Word file.`

func (a *Agent) createWordEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_word", createWordPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":     map[string]any{"type": "string", "description": `File name for the document, e.g. "Q1 report" (".docx" is added). It is not printed: start the Markdown with a "# Heading" for the visible title.`},
				"content":   map[string]any{"type": "string", "description": "The text itself (Markdown, or HTML with format \"html\"). Omit when source is given."},
				"source":    map[string]any{"type": "string", "description": "Where to read the text from instead: a sandbox file (tmp://report.md, storage://report.html) or a saved text document (u.12). Omit when content is given."},
				"format":    map[string]any{"type": "string", "enum": []string{"markdown", "html"}, "description": "What the text is. Default markdown (html when source ends in .html or the text starts with <!DOCTYPE or <html)."},
				"landscape": map[string]any{"type": "boolean", "description": "Landscape A4 pages (for wide tables). Default false: portrait."},
				"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
			},
			"required": []string{"title"},
		}),
		handler: a.createWord,
	}
}

func (a *Agent) createWord(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.Word == nil {
		return wordNotSetUp, nil
	}
	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		return "error: title is required", nil
	}
	if !strings.HasSuffix(strings.ToLower(title), ".docx") {
		title += ".docx"
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
	scope, db, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	gs := genSource{Kind: genWord, Format: formatName(isHTML), Landscape: landscape, Source: src}
	g, err := a.generate(ctx, title, gs, true)
	if err != nil {
		if _, conv := err.(convertError); conv {
			return "error: " + err.Error() + " — pass the text as content, or as source (a sandbox file or a saved document)", nil
		}
		return "error: " + err.Error(), nil
	}
	doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://create_word", Mime: docxMime, Content: g.text,
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeGenerated(ctx, db, scope, rc, doc, g.data, ".docx", gs)
	a.publishDocChange(docevents.Created, scope, rc, doc.ID)
	return fmt.Sprintf("Created Word document %s.%d %q. The user can open or download it from their files.%s", scopeLetter(scope), doc.ID, title, g.notes), nil
}
