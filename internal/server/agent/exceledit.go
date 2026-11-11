package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/mdconv"
	"github.com/ivoras/harlequin/internal/server/xlsxedit"
)

// xlsxOp maps the shared edit operation onto xlsxedit's.
func xlsxOp(op textEditOp) (xlsxedit.Op, string) {
	switch {
	case op.hasDiff:
		return xlsxedit.Op{}, "error: diff is not available for Excel files; use cells (set cells), append or insert (table rows), or replace."
	case op.hasCells:
		return xlsxedit.Op{Kind: xlsxedit.SetCells, Sheet: op.sheet, Cells: op.cells}, ""
	case op.hasReplace:
		return xlsxedit.Op{Kind: xlsxedit.Replace, Sheet: op.sheet, Old: op.oldString, New: op.newString, ReplaceAll: op.replaceAll}, ""
	case op.hasInsert:
		return xlsxedit.Op{Kind: xlsxedit.InsertRows, Sheet: op.sheet, Text: op.insertText, Anchor: op.anchor, Before: op.insertBefore}, ""
	}
	return xlsxedit.Op{Kind: xlsxedit.AppendRows, Sheet: op.sheet, Text: op.appendText}, ""
}

// xlsxText derives a workbook's searchable text the way an upload does: the
// configured converter (docling/xberg) when there is one and it succeeds, else
// the built-in mdconv.
func (a *Agent) xlsxText(ctx context.Context, path string, data []byte) (string, error) {
	name := filepath.Base(path)
	if !strings.HasSuffix(strings.ToLower(name), ".xlsx") {
		name += ".xlsx"
	}
	if a.MarkdownConverter != nil {
		md, err := a.MarkdownConverter.Convert(ctx, name, data)
		if err == nil && strings.TrimSpace(md) != "" {
			return md, nil
		}
		log.Printf("documents: converter failed on edited %q, using the built-in converter: %v", name, err)
	}
	return mdconv.Convert(name, data)
}

// editExcelDoc edits an uploaded .xlsx inside the original file, so styles,
// number formats, formulas and the other sheets stay as they were.
func (a *Agent) editExcelDoc(ctx context.Context, e *docEdit) (string, error) {
	data, err := os.ReadFile(e.path)
	if err != nil {
		return fmt.Sprintf("error: cannot read the stored file of %s for editing.", e.ref), nil
	}
	op, msg := xlsxOp(e.op)
	if msg != "" {
		return msg, nil
	}
	out, what, err := xlsxedit.Edit(data, op)
	if err != nil {
		return "error: " + err.Error() + ". Nothing was changed.", nil
	}
	text, err := a.xlsxText(ctx, e.path, out)
	if err != nil {
		return "error: the edited workbook could not be read back (" + err.Error() + "). Nothing was changed.", nil
	}
	chunks, warn, err := a.commitDocEdit(ctx, e, text, out, nil)
	if err != nil {
		return "", err
	}
	if warn != "" {
		return "error: " + warn, nil
	}
	return fmt.Sprintf("Edited Excel workbook %s (%q): %s%s. The original formatting, formulas and other sheets were kept, and Excel recalculates formulas when the file is opened; the text is now %d characters in %d sections, re-indexed for search_docs.", e.ref, e.doc.Title, what, e.from, utf8.RuneCountInString(text), chunks), nil
}
