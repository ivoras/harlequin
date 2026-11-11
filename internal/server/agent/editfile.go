package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

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
		"append_source": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Append from: documents and/or sandbox files whose whole text is added at the end, in order, e.g. ["u.18", "tmp://annex.md"]. The text is read on the server and does not pass through you.`},
	}
	for k, v := range target {
		props[k] = v
	}
	return map[string]any{"type": "object", "properties": props}
}

const editOpsHelp = `It can do four things — give exactly ONE per call: (1) REPLACE: old_string + new_string — exact replace (old_string must be unique unless replace_all is true; include enough surrounding text; an empty new_string deletes); (2) PATCH: diff — a unified diff, best for several changes at once; (3) APPEND: append — add text you write at the end; (4) APPEND FROM: append_source — add the whole text of other documents and/or sandbox files at the end (concatenation; any length, since the text does not pass through you; for a PDF, Word or Excel source its Markdown text is used; sources are not changed). The edit is all-or-nothing: on any error nothing is changed and the message says what to fix.`

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

// editDocEntry registers edit_doc: apply an edit to a saved corpus document.
func (a *Agent) editDocEntry() toolEntry {
	schema := editArgsSchema(map[string]any{
		"doc": map[string]any{"type": "string", "description": "Scoped document id, e.g. \"u.12\", \"s.3\" or \"p.7\" (resolve with list_documents); or a sandbox file such as tmp://notes.md"},
	})
	schema["required"] = []string{"doc"}
	return toolEntry{
		def: fnTool("edit_doc", `Edit a saved text/Markdown document in the corpus in place, keeping its id, title and scope — for fixing a passage in, adding text to the end of, or appending other documents to a document you (or the user) saved with save_doc or uploaded as .md/.txt. `+editOpsHelp+` Given a sandbox file (tmp://notes.md) as doc it edits that file, like edit_file. The document is re-indexed afterwards, so search_docs sees the new text; its chunk ids change, so [d.x.N] chunk citations pointing INTO this document may no longer match (document ids stay valid). Documents whose original is a PDF/DOCX/etc. cannot be edited — save an edited copy with save_doc instead. Edit only when the user asked you to change that document.`, schema),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			if p := argString(args, "doc"); isSandboxPath(p) { // a sandbox file: edit_file's job
				return a.editFileEntry().handler(ctx, rc, withArg(args, "path", strings.TrimSpace(p)))
			}
			from := ""
			if add, what, ok, errMsg := a.appendSourceArg(ctx, rc, args); errMsg != "" {
				return errMsg, nil
			} else if ok {
				args, from = withArg(args, "append", "\n"+add), " from "+what
			}
			op, err := parseTextEditOp(args)
			if err != nil {
				return "error: " + err.Error(), nil
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
			var filesDir string
			if a.Storage != nil {
				switch doc.Scope {
				case documents.ScopeShared:
					filesDir, _ = a.Storage.SharedFilesDir()
				case documents.ScopeProject:
					filesDir, _ = a.Storage.ProjectFilesDir(rc.projectID)
				default:
					filesDir, _ = a.Storage.UserFilesDir(rc.userID)
				}
			}
			ref := fmt.Sprintf("%s.%d", scopeLetter(doc.Scope), doc.ID)
			stored, mime, _, err := a.Docs.StoredFile(ctx, db, doc.ID)
			if err != nil {
				return "", err
			}
			// Only documents with a stored UTF-8 text original are editable: the
			// indexed chunks overlap and normalise whitespace, so they are not a
			// faithful source to edit from.
			if stored == "" || filesDir == "" {
				return fmt.Sprintf("error: document %s has no stored text original, so it cannot be edited in place; save a corrected copy with save_doc.", ref), nil
			}
			if !strings.HasPrefix(mime, "text/") {
				return fmt.Sprintf("error: document %s is %s, not text/Markdown, so it cannot be edited in place; save an edited copy with save_doc.", ref, mime), nil
			}
			path := filepath.Join(filesDir, filepath.Base(stored))
			data, err := os.ReadFile(path)
			if err != nil || !utf8.Valid(data) {
				return fmt.Sprintf("error: cannot read the stored text of %s for editing.", ref), nil
			}
			out, what, err := op.apply(string(data))
			if err != nil {
				return "error: " + err.Error(), nil
			}
			out = documents.StripSoftHyphens(out)
			if strings.TrimSpace(out) == "" {
				return "error: the edit would leave the document empty; use delete_doc to remove it.", nil
			}
			if doc.Scope == documents.ScopeProject && rc.projectID != 0 {
				out = pinProjectCitations(out, rc.projectID)
			}
			// Re-index first (a failure leaves the old chunks and file untouched),
			// then replace the stored original.
			chunks, err := a.Docs.ReplaceContent(ctx, db, doc.ID, out)
			if err != nil {
				return "", err
			}
			a.publishDocChange(docevents.Updated, doc.Scope, rc, doc.ID)
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, []byte(out), 0o600); err == nil {
				err = os.Rename(tmp, path)
			}
			if err != nil {
				return fmt.Sprintf("Edited and re-indexed %s (%s, %d sections), but could not update its stored file (%v); view/download will show the old text.", ref, what, chunks, err), nil
			}
			return fmt.Sprintf("Edited document %s (%q): %s%s; now %d characters in %d sections, re-indexed for search_docs.", ref, doc.Title, what, from, utf8.RuneCountInString(out), chunks), nil
		},
	}
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
