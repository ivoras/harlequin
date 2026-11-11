package agent

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docalign"
	"github.com/ivoras/harlequin/internal/server/docevents"
	"github.com/ivoras/harlequin/internal/server/documents"
)

// docSourceRE recognises a document reference where a sandbox path could also
// stand: u.12, s.3, p.7, p3.7, or as attached in a prompt (@"u.18/report.md").
var docSourceRE = regexp.MustCompile(`^@?"?(?:d\.)?(?:[us]|p\d*)\.\d+(?:[/" ]|$)`)

// docFilesDir is where the stored originals of scope's documents live.
func (a *Agent) docFilesDir(rc *runContext, scope string) string {
	if a.Storage == nil {
		return ""
	}
	var dir string
	if idStr, ok := strings.CutPrefix(scope, documents.ScopeProject+":"); ok {
		pid, _ := strconv.ParseInt(idStr, 10, 64)
		dir, _ = a.Storage.ProjectFilesDir(pid)
		return dir
	}
	switch scope {
	case documents.ScopeShared:
		dir, _ = a.Storage.SharedFilesDir()
	case documents.ScopeProject:
		dir, _ = a.Storage.ProjectFilesDir(rc.projectID)
	default:
		dir, _ = a.Storage.UserFilesDir(rc.userID)
	}
	return dir
}

// docText returns a document's text: its stored original when that is text
// (exact, as saved), else its Markdown rendering rebuilt from the index (what
// read_doc shows for a PDF, DOCX or spreadsheet).
func (a *Agent) docText(ctx context.Context, rc *runContext, doc *docalign.Doc) (string, string) {
	db, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
	if errMsg != "" {
		return "", errMsg
	}
	if stored, mime, _, err := a.Docs.StoredFile(ctx, db, doc.ID); err == nil && stored != "" && strings.HasPrefix(mime, "text/") {
		if dir := a.docFilesDir(rc, doc.Scope); dir != "" {
			if data, err := os.ReadFile(filepath.Join(dir, filepath.Base(stored))); err == nil && utf8.Valid(data) {
				return string(data), ""
			}
		}
	}
	text, err := a.Docs.FullText(ctx, db, doc.ID)
	if err != nil {
		return "", fmt.Sprintf("error: cannot read document %s: %v", docRef(doc), err)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Sprintf("error: document %s has no text", docRef(doc))
	}
	return text, ""
}

// isSandboxPath tells a sandbox file from a document reference wherever a tool
// takes either: tmp://… and storage://… always, and a bare name that looks like
// a file (it has an extension or a directory) and not like a document id — a
// bare path means tmp://, as everywhere in the sandbox tools.
func isSandboxPath(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "tmp://") || strings.HasPrefix(s, "storage://") {
		return true
	}
	return s != "" && !strings.Contains(s, "://") && !docSourceRE.MatchString(s) &&
		strings.ContainsAny(s, "./") && !strings.HasPrefix(s, "@")
}

// sourceText reads the text src names: a sandbox file (tmp://, storage://, or a
// bare path) or a saved document (u.12). label describes it for the tool result.
func (a *Agent) sourceText(ctx context.Context, rc *runContext, src string) (text, label, errMsg string) {
	src = strings.TrimSpace(src)
	if !isSandboxPath(src) {
		doc, errMsg := a.loadDocRef(ctx, rc, src)
		if errMsg != "" {
			return "", "", errMsg
		}
		text, errMsg = a.docText(ctx, rc, doc)
		return text, fmt.Sprintf("document %s (%q)", docRef(doc), doc.Title), errMsg
	}
	root, _, rel, err := a.resolveNamespaceRoot(rc, src)
	if err != nil {
		return "", "", "error: " + err.Error()
	}
	data, err := root.Read(rel)
	if err != nil {
		return "", "", fmt.Sprintf("error: cannot read source %s: %v", src, err)
	}
	if !utf8.Valid(data) {
		return "", "", fmt.Sprintf("error: source %s is not UTF-8 text", src)
	}
	return string(data), src, ""
}

// argStrings reads an argument that is a list of strings, tolerating a single
// string (small models often send one where a list is declared).
func argStrings(args map[string]any, key string) []string {
	var out []string
	switch v := args[key].(type) {
	case string:
		out = append(out, v)
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = v
	}
	kept := out[:0]
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			kept = append(kept, s)
		}
	}
	return kept
}

// appendSourceArg turns an edit tool's append_source (documents and/or sandbox
// files, added in order with an empty line between them) into the text to
// append; ok is false when the argument is absent.
func (a *Agent) appendSourceArg(ctx context.Context, rc *runContext, args map[string]any) (text, what string, ok bool, errMsg string) {
	sources := argStrings(args, "append_source")
	if len(sources) == 0 {
		return "", "", false, ""
	}
	if argString(args, "append") != "" {
		return "", "", true, "error: give append (text you write) or append_source (documents/files to add), not both"
	}
	var parts, labels []string
	for _, src := range sources {
		t, label, errMsg := a.sourceText(ctx, rc, src)
		if errMsg != "" {
			return "", "", true, errMsg
		}
		if t = strings.Trim(t, "\r\n"); strings.TrimSpace(t) == "" {
			return "", "", true, fmt.Sprintf("error: source %s is empty", src)
		}
		parts = append(parts, t)
		labels = append(labels, label)
	}
	return strings.Join(parts, "\n\n") + "\n", strings.Join(labels, ", "), true, ""
}

func (a *Agent) copyDocEntry() toolEntry {
	return toolEntry{
		def: fnTool("copy_doc", `Copy a document or a sandbox file, on the server (the content does not pass through you). source is a saved document (u.12, s.3, p.7) or a sandbox file (tmp://a.md, storage://a.md; a bare path such as a.md means tmp://). Where the copy goes: (1) give target, a sandbox file path (tmp://work.md), to get a working file you can then change with edit_file or Grep — a document is copied as its text (for a PDF, Word or Excel document: its Markdown text, not the original file); (2) omit target to make a new saved document — a document is duplicated exactly, original file included (a PDF stays a PDF), and a sandbox text file becomes a new text document like save_doc. title names the new document (default: "Copy of …" or the file name); it lands in the project in a project session, else in the user's personal files, or where scope says (shared: owner/admin only). The source is never changed. To add one document to the end of another use edit_doc (or edit_file) with append_source.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":    map[string]any{"type": "string", "description": `What to copy: a document id ("u.12") or a sandbox file ("tmp://a.md")`},
				"target":    map[string]any{"type": "string", "description": `Sandbox file to copy to, e.g. "tmp://work.md". Omit to create a new saved document instead.`},
				"overwrite": map[string]any{"type": "boolean", "description": "Replace target if it already exists. Default false: an existing file is an error."},
				"title":     map[string]any{"type": "string", "description": "Title of the new document (only without target)"},
				"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}, "description": "Where the new document goes (only without target)"},
			},
			"required": []string{"source"},
		}),
		handler: a.copyDoc,
	}
}

func (a *Agent) copyDoc(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	src := strings.TrimSpace(argString(args, "source"))
	target := strings.TrimSpace(argString(args, "target"))
	if src == "" {
		return `error: source is required: a document id ("u.12") or a sandbox file ("tmp://a.md")`, nil
	}
	srcIsDoc := !isSandboxPath(src)

	if target != "" {
		if !isSandboxPath(target) {
			return "error: target must be a sandbox file (tmp://… or storage://…). To add a document to the end of an existing one use edit_doc with append_source; to make a new document omit target.", nil
		}
		root, _, rel, err := a.resolveNamespaceRoot(rc, target)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if rel == "" {
			return "error: target must name a file, e.g. tmp://work.md", nil
		}
		if exists, _ := root.Exists(rel); exists {
			if over, _ := args["overwrite"].(bool); !over {
				return fmt.Sprintf("error: %s already exists; choose another name, or pass overwrite: true to replace it", target), nil
			}
		}
		var data []byte
		var label string
		if srcIsDoc {
			text, l, errMsg := a.sourceText(ctx, rc, src)
			if errMsg != "" {
				return errMsg, nil
			}
			data, label = []byte(text), l+" as text"
		} else { // file to file: any content, byte for byte
			sroot, _, srel, err := a.resolveNamespaceRoot(rc, src)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			if data, err = sroot.Read(srel); err != nil {
				return fmt.Sprintf("error: cannot read source %s: %v", src, err), nil
			}
			label = src
		}
		if err := root.Write(rel, data); err != nil {
			return fmt.Sprintf("error: cannot write %s: %v", target, err), nil
		}
		return fmt.Sprintf("Copied %s to %s (%d bytes).", label, target, len(data)), nil
	}

	title := strings.TrimSpace(argString(args, "title"))
	if !srcIsDoc {
		// A sandbox file becomes a document the way save_doc makes one.
		if title == "" {
			title = path.Base(src)
		}
		save := map[string]any{"title": title, "source": src, "is_raw": true}
		if s, ok := args["scope"].(string); ok && s != "" {
			save["scope"] = s
		}
		return a.buildTools(ctx, rc)["save_doc"].handler(ctx, rc, save)
	}

	doc, errMsg := a.loadDocRef(ctx, rc, src)
	if errMsg != "" {
		return errMsg, nil
	}
	srcDB, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
	if errMsg != "" {
		return errMsg, nil
	}
	scope, dstDB, errMsg := a.saveScope(rc, args)
	if errMsg != "" {
		return errMsg, nil
	}
	if title == "" {
		title = doc.Title
		if scope == doc.Scope {
			title = "Copy of " + doc.Title
		}
	}
	newID, err := a.Docs.CopyInto(ctx, srcDB, dstDB, a.docFilesDir(rc, doc.Scope), a.docFilesDir(rc, scope), doc.ID)
	if err != nil {
		return "", err
	}
	if err := a.Docs.SetTitle(ctx, dstDB, newID, title); err != nil {
		return "", err
	}
	a.publishDocChange(docevents.Created, scope, rc, newID)
	return fmt.Sprintf("Copied document %s (%q) to new document %s.%d %q; it is searchable with search_docs and the original is unchanged.",
		docRef(doc), doc.Title, scopeLetter(scope), newID, title), nil
}
