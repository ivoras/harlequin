package agent

import (
	"cmp"
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
	"github.com/ivoras/harlequin/internal/server/sheetdb"
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
			if data, err := os.ReadFile(filepath.Join(dir, filepath.Base(stored))); err == nil {
				if mime == "text/csv" || mime == "text/tab-separated-values" {
					data = sheetdb.DecodeText(data) // kept as uploaded, often Windows-1250
				}
				if utf8.Valid(data) {
					return string(data), ""
				}
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

// isFilePath tells a file (see refs.go) from a document reference wherever a
// tool takes either: tmp://…, storage://… and skill://… always, and a bare name
// that looks like a file (it has an extension or a directory) and not like a
// document id — a bare path means tmp://, as everywhere in the sandbox tools.
func isFilePath(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "tmp://") || strings.HasPrefix(s, "storage://") || strings.HasPrefix(s, "skill://") {
		return true
	}
	return s != "" && !strings.Contains(s, "://") && !docSourceRE.MatchString(s) &&
		strings.ContainsAny(s, "./") && !strings.HasPrefix(s, "@")
}

// sourceText reads the text src names: a file (tmp://, storage://, a bare path,
// skill://) or a saved document (u.12). label describes it for the tool result.
func (a *Agent) sourceText(ctx context.Context, rc *runContext, src string) (text, label, errMsg string) {
	src = strings.TrimSpace(src)
	if !isFilePath(src) {
		doc, errMsg := a.loadDocRef(ctx, rc, src)
		if errMsg != "" {
			return "", "", errMsg
		}
		text, errMsg = a.docText(ctx, rc, doc)
		return text, fmt.Sprintf("document %s (%q)", docRef(doc), doc.Title), errMsg
	}
	data, err := a.readFile(ctx, rc, src)
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

// binaryExts are target names that promise a file format, never plain text.
var binaryExts = map[string]bool{
	".pdf": true, ".docx": true, ".doc": true, ".xlsx": true, ".xls": true, ".pptx": true,
	".odt": true, ".ods": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".zip": true,
}

// docOriginal returns a document's stored original file and its extension when
// that is not text (a PDF, DOCX, XLSX …); data is nil for text documents and
// those without a stored file. label names the document for tool results.
func (a *Agent) docOriginal(ctx context.Context, rc *runContext, ref string) (data []byte, ext, label, errMsg string) {
	doc, errMsg := a.loadDocRef(ctx, rc, ref)
	if errMsg != "" {
		return nil, "", "", errMsg
	}
	label = fmt.Sprintf("document %s (%q)", docRef(doc), doc.Title)
	db, errMsg := a.docDBForScope(ctx, rc, doc.Scope)
	if errMsg != "" {
		return nil, "", "", errMsg
	}
	stored, mime, _, err := a.Docs.StoredFile(ctx, db, doc.ID)
	dir := a.docFilesDir(rc, doc.Scope)
	if err != nil || stored == "" || strings.HasPrefix(mime, "text/") || dir == "" {
		return nil, "", label, ""
	}
	if data, err = os.ReadFile(filepath.Join(dir, filepath.Base(stored))); err != nil {
		return nil, "", "", fmt.Sprintf("error: cannot read the original file of %s: %v", label, err)
	}
	return data, strings.ToLower(filepath.Ext(stored)), label, ""
}

func (a *Agent) copyDocEntry() toolEntry {
	return toolEntry{
		def: fnTool("copy_doc", `Copy a document or a file, on the server (the content does not pass through you). source is a saved document (u.12, s.3, p.7) or a file (tmp://a.md, storage://a.md, a bare path such as a.md meaning tmp://, or skill://<skill>/<path>). Where the copy goes: (1) give target, a file path (tmp://work.md; or skill://<skill>/<path> to put a text into a skill, only when the user asks to change the skill — scope then says whose copy), to get a working file you can then change with edit_doc or Grep — a document is copied as its text (for a PDF, Word or Excel document: its Markdown text), unless target has the original file's extension (tmp://a.pdf for a PDF, .docx for a Word document, .xlsx …): then the original file is copied byte for byte; (2) omit target to make a new saved document — a document is duplicated exactly, original file included (a PDF stays a PDF), and a sandbox text file becomes a new text document like save_doc. title names the new document (default: "Copy of …" or the file name); it lands in the project in a project session, else in the user's personal files, or where scope says (shared: owner/admin only). The source is never changed. To add one document to the end of another use edit_doc with append_source.`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":    map[string]any{"type": "string", "description": `What to copy: a document id ("u.12") or a file ("tmp://a.md", "skill://weekly-report/SKILL.md")`},
				"target":    map[string]any{"type": "string", "description": `File to copy to, e.g. "tmp://work.md". Omit to create a new saved document instead.`},
				"overwrite": map[string]any{"type": "boolean", "description": "Replace target if it already exists. Default false: an existing file is an error."},
				"title":     map[string]any{"type": "string", "description": "Title of the new document (only without target)"},
				"scope":     map[string]any{"type": "string", "enum": []string{"personal", "project", "shared"}, "description": "Where the new document goes (without target); for a skill:// target, whose copy of the skill is written (default personal)"},
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
		return `error: source is required: a document id ("u.12") or a file ("tmp://a.md")`, nil
	}
	srcIsDoc := !isFilePath(src)

	if target != "" {
		if !isFilePath(target) {
			return "error: target must be a file (tmp://…, storage://… or skill://…). To add a document to the end of an existing one use edit_doc with append_source; to make a new document omit target.", nil
		}
		_, _, rel, err := a.fileRoot(ctx, rc, target)
		if err != nil && !isSkillPath(target) { // a new skill's files fail to resolve; writeFile says why
			return "error: " + err.Error(), nil
		}
		if isSkillPath(target) {
			_, rel, _ = splitSkillPath(target)
		}
		if rel == "" {
			return "error: target must name a file, e.g. tmp://work.md", nil
		}
		if a.fileExists(ctx, rc, target) {
			if over, _ := args["overwrite"].(bool); !over {
				return fmt.Sprintf("error: %s already exists; choose another name, or pass overwrite: true to replace it", target), nil
			}
		}
		var data []byte
		var label string
		if srcIsDoc {
			orig, origExt, l, errMsg := a.docOriginal(ctx, rc, src)
			if errMsg != "" {
				return errMsg, nil
			}
			ext := strings.ToLower(path.Ext(rel))
			switch {
			case orig != nil && ext == origExt:
				data, label = orig, l+" (original file)"
			case binaryExts[ext]:
				what := "no original file, only text"
				if orig != nil {
					what = "a " + origExt + " original"
				}
				return fmt.Sprintf("error: %s has %s, so it cannot be copied to a %s file. Name the target %s to get the original file, or .md / .txt to get its text.", l, what, ext, cmp.Or(origExt, ".md")), nil
			default:
				text, l, errMsg := a.sourceText(ctx, rc, src)
				if errMsg != "" {
					return errMsg, nil
				}
				data, label = []byte(text), l+" as text"
			}
		} else { // file to file: any content, byte for byte
			var err error
			if data, err = a.readFile(ctx, rc, src); err != nil {
				return fmt.Sprintf("error: cannot read source %s: %v", src, err), nil
			}
			label = src
		}
		saved, errMsg := a.writeFile(ctx, rc, target, data, true, argString(args, "scope"))
		if errMsg != "" {
			return errMsg, nil
		}
		out := fmt.Sprintf("Copied %s to %s (%d bytes).", label, target, len(data))
		if isSkillPath(target) {
			out += " " + saved
		}
		return out, nil
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
