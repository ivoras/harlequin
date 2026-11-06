package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
	"github.com/ivoras/harlequin/internal/server/mdxlsx"
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
				"source":  map[string]any{"type": "string", "description": "Sandbox file holding the Markdown instead, e.g. tmp://sales.md. Omit when content is given."},
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
	md := argString(args, "content")
	if src := strings.TrimSpace(argString(args, "source")); src != "" {
		if strings.TrimSpace(md) != "" {
			return "error: pass either content or source, not both", nil
		}
		root, _, rel, err := a.resolveNamespaceRoot(rc, src)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		data, err := root.Read(rel)
		if err != nil {
			return fmt.Sprintf("error: cannot read source %s: %v", src, err), nil
		}
		if !utf8.Valid(data) {
			return fmt.Sprintf("error: source %s is not UTF-8 text", src), nil
		}
		md = string(data)
	}
	xlsx, st, err := mdxlsx.Convert(md)
	if err != nil {
		return "error: " + err.Error() + ` — each table row must be a line starting with "|"; see load_skill "create-excel"`, nil
	}

	scope, _ := args["scope"].(string)
	if scope == "" {
		scope = documents.ScopePersonal
		if rc.projectDB != nil {
			scope = documents.ScopeProject
		}
	}
	db := rc.userDB
	switch scope {
	case documents.ScopePersonal:
	case documents.ScopeProject:
		if rc.projectDB == nil {
			return `error: project scope is only available in a project session; use scope "personal"`, nil
		}
		db = rc.projectDB
	case documents.ScopeShared:
		if !rc.canShareMemory {
			return `error: only owner or admin users can save shared documents; use scope "personal" instead`, nil
		}
		db = a.Docs.SharedDB()
	default:
		return "error: invalid scope (personal|project|shared)", nil
	}
	// The searchable text is the Markdown itself — what docling would extract
	// from this workbook — so search_docs and read_doc work on it at once.
	doc, err := a.Docs.IngestInto(ctx, db, types.CreateDocumentRequest{
		Title: title, URI: "agent://create_excel", Mime: xlsxMime, Content: strings.TrimSpace(md),
	}, rc.userID)
	if err != nil {
		return "", err
	}
	a.storeDocFile(ctx, db, scope, rc, doc, xlsx, ".xlsx")
	a.publishDocChange(docevents.Created, scope, rc, doc.ID)
	msg := fmt.Sprintf("Created Excel workbook %s.%d %q: %d sheet(s), %d table(s), %d row(s). The user can open or download it from their files.",
		scopeLetter(scope), doc.ID, title, st.Sheets, st.Tables, st.Rows)
	if st.Ignored > 0 {
		msg += fmt.Sprintf(" Note: %d non-table line(s) were left out (only headings and tables go into the workbook).", st.Ignored)
	}
	return msg, nil
}
