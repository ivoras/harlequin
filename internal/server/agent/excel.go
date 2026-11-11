package agent

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/shared/types"
)

const xlsxMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

const createExcelPrompt = `Create an Excel workbook (.xlsx) from Markdown tables and save it as a document the user can download — the reverse of how an uploaded spreadsheet is read. load_skill "create-excel" first for the exact Markdown format. In short: "# Sheet name" headings start sheets; each pipe table's first row is a bold header; tables on one sheet are stacked with an empty row between; plain numbers (1500, 0.95 — no thousands separators or currency signs) become numeric cells and "=SUM(B2:B9)" becomes a formula; anything else is text. Pass the Markdown inline as content, or — for anything long — as source: a sandbox file such as "tmp://report.md" (build it with edit_file append). The workbook lands in the project in a project session, else in the user's personal files; scope "shared" is owner/admin only.`

func (a *Agent) createExcelEntry() toolEntry {
	return toolEntry{
		def: fnTool("create_excel", createExcelPrompt, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":   map[string]any{"type": "string", "description": `File name for the workbook, e.g. "Q1 sales" (".xlsx" is added)`},
				"content": map[string]any{"type": "string", "description": "The Markdown (headings + pipe tables). Omit when source is given."},
				"source":  map[string]any{"type": "string", "description": "Where to read the Markdown from instead: a sandbox file (tmp://sales.md, storage://sales.md) or a saved document (u.12). Omit when content is given."},
				"scope":   map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}},
			},
			"required": []string{"title"},
		}),
		handler: a.createExcel,
	}
}

func (a *Agent) createExcel(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	title := strings.TrimSpace(argString(args, "title"))
	if title == "" {
		return "error: title is required", nil
	}
	if !strings.HasSuffix(strings.ToLower(title), ".xlsx") {
		title += ".xlsx"
	}
	md, errMsg := a.markdownArg(ctx, rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	scope, db, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	gs := genSource{Kind: genExcel, Format: "markdown", Source: md}
	g, err := a.generate(ctx, title, gs, true)
	if err != nil {
		return "error: " + err.Error() + ` — each table row must be a line starting with "|"; see load_skill "create-excel"`, nil
	}
	// The searchable text is the Markdown itself — what docling would extract
	// from this workbook — so search_docs and read_doc work on it at once.
	doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://create_excel", Mime: xlsxMime, Content: g.text,
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeGenerated(ctx, db, scope, rc, doc, g.data, ".xlsx", gs)
	a.publishDocChange(docevents.Created, scope, rc, doc.ID)
	return fmt.Sprintf("Created Excel workbook %s.%d %q%s. The user can open or download it from their files.%s",
		scopeLetter(scope), doc.ID, title, g.detail, g.notes), nil
}

// markdownArg returns a file-creating tool's text: the inline content, or what
// source names — a sandbox file (tmp://, storage://) or a saved document.
// errMsg is the tool result on bad input.
func (a *Agent) markdownArg(ctx context.Context, rc *runContext, args map[string]any) (md, errMsg string) {
	md = argString(args, "content")
	src := strings.TrimSpace(argString(args, "source"))
	if src == "" {
		return md, ""
	}
	if strings.TrimSpace(md) != "" {
		return "", "error: pass either content or source, not both"
	}
	md, _, errMsg = a.sourceText(ctx, rc, src)
	return md, errMsg
}

// saveScope resolves where a created document goes: the scope argument, else
// the project in a project session, else the user's personal files.
func (a *Agent) saveScope(rc *runContext, args map[string]any) (scope string, db *sql.DB, errMsg string) {
	scope, _ = args["scope"].(string)
	if scope == "" {
		scope = documents.ScopePersonal
		if rc.projectDB != nil {
			scope = documents.ScopeProject
		}
	}
	switch scope {
	case documents.ScopePersonal:
		return scope, rc.userDB, ""
	case documents.ScopeProject:
		if rc.projectDB == nil {
			return "", nil, `error: project scope is only available in a project session; use scope "personal"`
		}
		return scope, rc.projectDB, ""
	case documents.ScopeShared:
		if !rc.canShareMemory {
			return "", nil, `error: only owner or admin users can save shared documents; use scope "personal" instead`
		}
		return scope, a.Docs.SharedDB(), ""
	}
	return "", nil, "error: invalid scope (personal|project|shared)"
}
