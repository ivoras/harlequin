package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
)

// editArgsSchema is the operation part shared by edit_file and edit_doc.
func editArgsSchema(target map[string]any) map[string]any {
	props := map[string]any{
		"old_string":    map[string]any{"type": "string", "description": "Replace: the exact text to replace (must occur once unless replace_all). Use with new_string."},
		"new_string":    map[string]any{"type": "string", "description": "Replace: the replacement text (empty string deletes old_string)."},
		"replace_all":   map[string]any{"type": "boolean", "description": "Replace every occurrence of old_string instead of requiring exactly one."},
		"diff":          map[string]any{"type": "string", "description": "A unified diff ('@@ -a,b +c,d @@' hunks with ' ' context, '-' removed and '+' added lines). Hunks are located by their content, so the line numbers need not be exact; all hunks must apply or nothing changes."},
		"append":        map[string]any{"type": "string", "description": "Append: text you write, added at the end (on a new line if the text does not end with one)."},
		"insert":        map[string]any{"type": "string", "description": "Insert: the new text to add next to a passage. Use with exactly one of after or before."},
		"after":         map[string]any{"type": "string", "description": "Insert: a passage quoted exactly from the document (must occur once); the new text goes after the paragraph, table row or list item that holds it."},
		"before":        map[string]any{"type": "string", "description": "Insert: like after, but the new text goes before that paragraph, row or item."},
		"append_source": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Append from: documents and/or sandbox files whose whole text is added at the end, in order, e.g. ["u.18", "tmp://annex.md"]. The text is read on the server and does not pass through you.`},
	}
	for k, v := range target {
		props[k] = v
	}
	return map[string]any{"type": "object", "properties": props}
}

const editOpsHelp = `Give exactly ONE of these five operations per call. (1) REPLACE: old_string + new_string. The old_string must occur once in the text (or set replace_all). Copy it exactly and include enough surrounding text. An empty new_string deletes it. (2) PATCH: diff, a unified diff. Best for several changes at once. (3) APPEND: append, text you write, added at the end. (4) APPEND FROM: append_source, a list of documents and/or sandbox files whose whole text is added at the end. The text does not pass through you, so any length works; a PDF, Word or Excel source is read as its Markdown text; sources are not changed. (5) INSERT: insert + one of after or before. The after/before text is a passage you quote exactly from the document; it must occur once. Example: insert "New paragraph." after "Payment is due in 30 days." The new text becomes its own paragraph next to the one that holds the passage. A single table row ("| a | b |") or list item ("- text") quoted next to a row or item of the same kind is added on the next line, so adding rows and items works. For an uploaded Excel file edit_doc also has (6) CELLS: cells sets cells by address. The edit is all-or-nothing: on any error nothing is changed and the message says what to fix.`

// editFileEntry registers edit_file: apply an edit to a sandbox file.
func (a *Agent) editFileEntry() toolEntry {
	schema := editArgsSchema(map[string]any{
		"path": map[string]any{"type": "string", "description": "Sandbox file, e.g. tmp://combined.md or storage://notes.md (a bare path means tmp://)"},
	})
	schema["required"] = []string{"path"}
	return toolEntry{
		def: fnTool("edit_file", `Edit a text file in your sandbox (tmp:// or storage://; a bare path means tmp://) in place — without re-sending the whole file. `+editOpsHelp+` append and append_source create the file if it does not exist yet. Use it to build up a long document in pieces (append section by section), combine several documents into one file (append_source), fix a passage, or apply a diff; then save it with save_doc(source="tmp://..."). Given a document id (u.12) as path it edits that saved document, like edit_doc.`, schema),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			path := strings.TrimSpace(argString(args, "path"))
			if path == "" {
				return "error: path is required (e.g. tmp://combined.md)", nil
			}
			if docSourceRE.MatchString(path) && a.Docs != nil { // a document id: edit_doc's job
				return a.editDocEntry().handler(ctx, rc, withArg(args, "doc", path))
			}
			root, _, rel, err := a.resolveNamespaceRoot(rc, path)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			if rel == "" {
				return "error: path must name a file, not a namespace root", nil
			}
			exists, _ := root.Exists(rel)
			from := ""
			if add, what, ok, errMsg := a.appendSourceArg(ctx, rc, args); errMsg != "" {
				return errMsg, nil
			} else if ok {
				if exists {
					add = "\n" + add // an empty line between the old text and the new
				}
				args, from = withArg(args, "append", add), " from "+what
			}
			op, err := parseTextEditOp(args)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			var text string
			if exists {
				data, err := root.Read(rel)
				if err != nil {
					return fmt.Sprintf("error: cannot read %s: %v", path, err), nil
				}
				if !utf8.Valid(data) {
					return fmt.Sprintf("error: %s is not UTF-8 text and cannot be edited", path), nil
				}
				text = string(data)
			} else if !op.hasAppend {
				return fmt.Sprintf("error: %s does not exist (only append creates a file)", path), nil
			}
			out, what, err := op.apply(text)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			if err := root.Write(rel, []byte(out)); err != nil {
				return fmt.Sprintf("error: cannot write %s: %v", path, err), nil
			}
			return fmt.Sprintf("%s: %s%s; file is now %d characters.", path, what, from, utf8.RuneCountInString(out)), nil
		},
	}
}

// docEdit is what the edit_doc handlers need about the target document.
type docEdit struct {
	rc      *runContext
	db      *sql.DB
	doc     *docalign.Doc
	ref     string
	path    string // the stored original
	mime    string
	op      textEditOp
	from    string // " from <sources>" for append_source
	pinProj bool   // qualify bare project citations (text documents in a project)
}

// editOpArgs are the arguments that make up an edit operation (undo excludes them).
var editOpArgs = []string{"old_string", "new_string", "diff", "append", "append_source", "insert", "after", "before", "cells"}

// editDocEntry registers edit_doc: apply an edit to a saved corpus document.
func (a *Agent) editDocEntry() toolEntry {
	schema := editArgsSchema(map[string]any{
		"doc":   map[string]any{"type": "string", "description": "Scoped document id, e.g. \"u.12\", \"s.3\" or \"p.7\" (resolve with list_documents); or a sandbox file such as tmp://notes.md"},
		"cells": map[string]any{"type": "object", "description": `Uploaded Excel only: set cells. An object from cell address to value, e.g. {"October!B2": 1700, "C2": "=B2*0.73", "D2": "paid"}. A number sets a number, text starting with = a formula, other text text; null clears the cell.`},
		"sheet": map[string]any{"type": "string", "description": "Uploaded Excel only: the sheet for cells addresses without a sheet name, and for append, insert and replace. Not needed when the workbook has one sheet."},
		"undo":  map[string]any{"type": "boolean", "description": "Set true (with doc only, no other argument) to restore the version before the last edit. Only one step back is kept."},
	})
	schema["required"] = []string{"doc"}
	return toolEntry{
		def: fnTool("edit_doc", `Edit a saved document in place, keeping its id, title and scope. Use it when the user asks to change a document: fix a passage, add a paragraph, add text at the end, or append other documents. Works on: text/Markdown documents (save_doc, or uploaded .md/.txt); documents made with create_pdf, create_word or create_excel (their file is rebuilt from the edited Markdown); and uploaded Word (.docx) files, which are edited inside the original file so fonts, styles, headers and layout stay (replace, insert, append and append_source work; diff does not). For a Word file quote passages as read_doc shows them. A passage must lie in one paragraph. insert next to a table cell adds a paragraph in that cell; insert "| a | b |" next to a table row adds a row. Uploaded Excel (.xlsx) files are edited in place too, keeping formatting and formulas: FIRST call read_doc with cells true to see sheet names and cell addresses, then use cells (set cells, e.g. {"October!B2": 1700}), append (table rows "| a | b |" after the last row), insert (table rows after or before "row 5" or a quoted row) or replace (text and formulas only); diff does not work. Use the sheet argument when the workbook has several sheets. Uploaded PDF files cannot be edited. `+editOpsHelp+` To go back one step: edit_doc with doc and undo true. Given a sandbox file (tmp://notes.md) as doc it edits that file, like edit_file. The document is re-indexed afterwards, so search_docs sees the new text. Unchanged passages keep their chunk ids, so citations to them stay valid. Edit only when the user asked you to change that document.`, schema),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			undo := argBool(args, "undo", false)
			if p := argString(args, "doc"); isSandboxPath(p) { // a sandbox file: edit_file's job
				if undo {
					return "error: undo is not available for sandbox files (it restores saved documents)", nil
				}
				return a.editFileEntry().handler(ctx, rc, withArg(args, "path", strings.TrimSpace(p)))
			}
			e := &docEdit{rc: rc}
			if undo {
				for _, k := range editOpArgs {
					if v, ok := args[k]; ok && v != "" && v != nil {
						return "error: undo cannot be combined with another operation; call edit_doc with doc and undo only", nil
					}
				}
			} else {
				if add, what, ok, errMsg := a.appendSourceArg(ctx, rc, args); errMsg != "" {
					return errMsg, nil
				} else if ok {
					args, e.from = withArg(args, "append", "\n"+add), " from "+what
				}
				var err error
				if e.op, err = parseTextEditOp(args); err != nil {
					return "error: " + err.Error(), nil
				}
			}
			doc, errMsg := a.loadDocRef(ctx, rc, argString(args, "doc"))
			if errMsg != "" {
				return errMsg, nil
			}
			switch doc.Scope {
			case documents.ScopeShared:
				if !rc.canShareMemory {
					return "error: only owner or admin users can edit shared documents; ask an owner/admin, or save an edited personal copy with save_doc.", nil
				}
			case documents.ScopePersonal, documents.ScopeProject:
			default:
				return "error: this document belongs to another project's corpus and cannot be edited from this session.", nil
			}
			db, dbErrMsg := a.docDBForScope(ctx, rc, doc.Scope)
			if dbErrMsg != "" {
				return dbErrMsg, nil
			}
			filesDir := a.docFilesDir(rc, doc.Scope)
			e.db, e.doc, e.ref = db, doc, fmt.Sprintf("%s.%d", scopeLetter(doc.Scope), doc.ID)
			e.pinProj = doc.Scope == documents.ScopeProject && rc.projectID != 0
			stored, mime, _, err := a.Docs.StoredFile(ctx, db, doc.ID)
			if err != nil {
				return "", err
			}
			// Only documents with a stored original are editable: the indexed
			// chunks overlap and normalise whitespace, so they are not a faithful
			// source to edit from.
			if stored == "" || filesDir == "" {
				return fmt.Sprintf("error: document %s has no stored original, so it cannot be edited in place; save a corrected copy with save_doc.", e.ref), nil
			}
			e.path, e.mime = filepath.Join(filesDir, filepath.Base(stored)), mime
			if undo {
				return a.undoDocEdit(ctx, e)
			}
			// Where the document came from decides how it is edited.
			side, sideErr := os.ReadFile(e.path + sidecarExt)
			if e.op.hasCells && (sideErr == nil || mime != xlsxMime) {
				if sideErr == nil && mime == xlsxMime {
					return "error: this workbook is generated from Markdown; change it with replace, insert or append (cells only works on uploaded Excel files).", nil
				}
				return fmt.Sprintf("error: cells only works on an uploaded Excel (.xlsx) workbook; document %s is %s. Use replace, insert or append.", e.ref, mime), nil
			}
			switch {
			case sideErr == nil: // made by create_excel/create_pdf/create_word
				return a.editGeneratedDoc(ctx, e, side)
			case strings.HasPrefix(mime, "text/"):
				return a.editTextDoc(ctx, e)
			case mime == pdfMime:
				return fmt.Sprintf("error: document %s is an uploaded PDF, so it cannot be edited: its pages are fixed. To change it, make a new document from its text with create_pdf or create_word (source %q), or cut pages out with pdf_pages.", e.ref, e.ref), nil
			case mime == docxMime:
				return a.editWordDoc(ctx, e)
			case mime == xlsxMime:
				return a.editExcelDoc(ctx, e)
			}
			return fmt.Sprintf("error: document %s is %s, not text/Markdown, so it cannot be edited in place; save an edited copy with save_doc.", e.ref, mime), nil
		},
	}
}

// editTextDoc edits a document whose stored original is UTF-8 text.
func (a *Agent) editTextDoc(ctx context.Context, e *docEdit) (string, error) {
	data, err := os.ReadFile(e.path)
	if err != nil || !utf8.Valid(data) {
		return fmt.Sprintf("error: cannot read the stored text of %s for editing.", e.ref), nil
	}
	out, what, err := e.op.apply(string(data))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	out = documents.StripSoftHyphens(out)
	if strings.TrimSpace(out) == "" {
		return "error: the edit would leave the document empty; use delete_doc to remove it.", nil
	}
	if e.pinProj {
		out = pinProjectCitations(out, e.rc.projectID)
	}
	chunks, warn, err := a.commitDocEdit(ctx, e, out, []byte(out), nil)
	if err != nil {
		return "", err
	}
	if warn != "" {
		return "error: " + warn, nil
	}
	return fmt.Sprintf("Edited document %s (%q): %s%s; now %d characters in %d sections, re-indexed for search_docs.", e.ref, e.doc.Title, what, e.from, utf8.RuneCountInString(out), chunks), nil
}

// editGeneratedDoc edits the source a PDF/Word/Excel document was generated
// from and rebuilds the file; any failure leaves the document as it was.
func (a *Agent) editGeneratedDoc(ctx context.Context, e *docEdit, side []byte) (string, error) {
	src, err := readSidecar(side)
	if err != nil {
		return fmt.Sprintf("error: document %s cannot be edited: %v. Make a new document with create_* instead.", e.ref, err), nil
	}
	if msg := a.notSetUp(src.Kind); msg != "" {
		return msg, nil
	}
	out, what, err := e.op.apply(src.Source)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	if strings.TrimSpace(out) == "" {
		return "error: the edit would leave the document empty; use delete_doc to remove it.", nil
	}
	src.Source = out
	g, err := a.generate(ctx, e.doc.Title, src, true)
	if err != nil {
		if _, conv := err.(convertError); conv {
			return fmt.Sprintf("error: the edited text cannot be converted to a %s file: %v. Nothing was changed; change the edit so the result is valid.", src.Kind, err), nil
		}
		return "error: " + err.Error() + ". Nothing was changed.", nil
	}
	newSide, err := json.Marshal(src)
	if err != nil {
		return "", err
	}
	chunks, warn, err := a.commitDocEdit(ctx, e, g.text, g.data, newSide)
	if err != nil {
		return "", err
	}
	if warn != "" {
		return "error: " + warn, nil
	}
	return fmt.Sprintf("Edited %s document %s (%q): %s%s; the file was rebuilt%s and re-indexed for search_docs (%d sections). The user's downloadable file now has the change.%s",
		src.Kind, e.ref, e.doc.Title, what, e.from, g.detail, chunks, g.notes), nil
}

// commitDocEdit replaces a document's indexed text and stored file (and, for a
// generated file, its source sidecar newSide), keeping the previous file(s) as
// .prev for one level of undo. The new files are staged first and the index is
// updated before they are moved into place, so a failure changes nothing; a
// non-empty warn is such a failure.
func (a *Agent) commitDocEdit(ctx context.Context, e *docEdit, text string, newFile, newSide []byte) (chunks int, warn string, err error) {
	side := e.path + sidecarExt
	type staged struct {
		path string
		data []byte
	}
	pairs := []staged{{e.path, newFile}}
	if newSide != nil {
		pairs = append(pairs, staged{side, newSide})
	}
	cleanup := func() {
		for _, p := range pairs {
			_ = os.Remove(p.path + ".tmp")
		}
	}
	for _, p := range pairs {
		if werr := os.WriteFile(p.path+".tmp", p.data, 0o600); werr != nil {
			cleanup()
			return 0, fmt.Sprintf("cannot write the stored file (%v); nothing was changed.", werr), nil
		}
	}
	if chunks, err = a.Docs.ReplaceContent(ctx, e.db, e.doc.ID, text); err != nil {
		cleanup()
		return 0, "", err
	}
	a.publishDocChange(docevents.Updated, e.doc.Scope, e.rc, e.doc.ID)
	for _, p := range pairs {
		path := p.path
		if old, rerr := os.ReadFile(path); rerr == nil {
			if werr := os.WriteFile(path+prevExt, old, 0o600); werr != nil {
				_ = os.Remove(e.path + prevExt) // never leave a .prev that does not match
				_ = os.Remove(side + prevExt)
			}
		}
		if rerr := os.Rename(path+".tmp", path); rerr != nil {
			return chunks, fmt.Sprintf("the document was re-indexed but its stored file could not be updated (%v); view/download will show the old file.", rerr), nil
		}
	}
	return chunks, "", nil
}

// undoDocEdit restores the version before the last edit: the stored file (and
// a generated file's source) come back from .prev, and the index follows.
func (a *Agent) undoDocEdit(ctx context.Context, e *docEdit) (string, error) {
	prev, err := os.ReadFile(e.path + prevExt)
	if err != nil {
		return fmt.Sprintf("error: document %s has no previous version to restore (only the version before the latest edit is kept, and each undo uses it up).", e.ref), nil
	}
	side := e.path + sidecarExt
	var text string
	sidePrev, serr := os.ReadFile(side + prevExt)
	if serr == nil {
		src, err := readSidecar(sidePrev)
		if err != nil {
			return fmt.Sprintf("error: cannot restore %s: the previous version's %v.", e.ref, err), nil
		}
		g, err := a.generate(ctx, e.doc.Title, src, false) // text only: the file itself comes back from .prev
		if err != nil {
			return fmt.Sprintf("error: cannot restore %s: %v", e.ref, err), nil
		}
		text = g.text
	} else if e.mime == xlsxMime { // an uploaded workbook: text comes from the restored file, as at upload
		if text, err = a.xlsxText(ctx, e.path, prev); err != nil {
			return fmt.Sprintf("error: cannot restore %s: %v", e.ref, err), nil
		}
	} else if e.mime == docxMime { // an uploaded Word file: text comes from the restored file, as at upload
		if text, err = a.docxText(ctx, e.path, prev); err != nil {
			return fmt.Sprintf("error: cannot restore %s: %v", e.ref, err), nil
		}
	} else if !utf8.Valid(prev) {
		return fmt.Sprintf("error: cannot read the previous text of %s.", e.ref), nil
	} else {
		text = string(prev)
	}
	if _, err := a.Docs.ReplaceContent(ctx, e.db, e.doc.ID, text); err != nil {
		return "", err
	}
	a.publishDocChange(docevents.Updated, e.doc.Scope, e.rc, e.doc.ID)
	for _, p := range []string{e.path, side} {
		if _, err := os.Stat(p + prevExt); err != nil {
			continue
		}
		if err := os.Rename(p+prevExt, p); err != nil {
			return fmt.Sprintf("error: the document %s was re-indexed from its previous version, but restoring its stored file failed (%v).", e.ref, err), nil
		}
	}
	return fmt.Sprintf("Restored the previous version of document %s (%q) and re-indexed it for search_docs. Undo keeps one step only, so there is nothing further to undo.", e.ref, e.doc.Title), nil
}

// withArg returns a copy of args with key set to val (the caller's map is the
// model's arguments, which are also logged; it is not modified).
func withArg(args map[string]any, key string, val any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		if k != "append_source" || key != "append" {
			out[k] = v
		}
	}
	out[key] = val
	return out
}
