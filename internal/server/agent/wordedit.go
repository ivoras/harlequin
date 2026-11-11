package agent

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/docxedit"
	"github.com/ivoras/harlequin/internal/server/docxextract"
)

// docxOp maps the shared edit operation onto docxedit's. An uploaded Word file
// is edited at paragraph level, so a unified diff has no meaning there.
func docxOp(op textEditOp) (docxedit.Op, string) {
	switch {
	case op.hasDiff:
		return docxedit.Op{}, "error: diff is not available for Word files; use replace (old_string+new_string), insert (insert+after or before), append, or append_source."
	case op.hasReplace:
		return docxedit.Op{Kind: docxedit.Replace, Old: op.oldString, New: op.newString, ReplaceAll: op.replaceAll}, ""
	case op.hasInsert:
		return docxedit.Op{Kind: docxedit.Insert, Text: op.insertText, Anchor: op.anchor, Before: op.insertBefore}, ""
	}
	return docxedit.Op{Kind: docxedit.Append, Text: op.appendText}, ""
}

// docxText derives a .docx's searchable text the way an upload does: the
// configured converter (docling/xberg) when there is one and it succeeds, else
// the built-in docxextract.
func (a *Agent) docxText(ctx context.Context, path string, data []byte) (string, error) {
	name := filepath.Base(path)
	if !strings.HasSuffix(strings.ToLower(name), ".docx") {
		name += ".docx"
	}
	if a.MarkdownConverter != nil {
		md, err := a.MarkdownConverter.Convert(ctx, name, data)
		if err == nil && strings.TrimSpace(md) != "" {
			return md, nil
		}
		log.Printf("documents: converter failed on edited %q, using the built-in extractor: %v", name, err)
	}
	text, err := docxextract.Text(data)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("the document has no extractable text")
	}
	return text, nil
}

// editWordDoc edits an uploaded .docx inside the original file, so formatting,
// styles, headers and everything else the edit does not touch stay as they were.
func (a *Agent) editWordDoc(ctx context.Context, e *docEdit) (string, error) {
	data, err := os.ReadFile(e.path)
	if err != nil {
		return fmt.Sprintf("error: cannot read the stored file of %s for editing.", e.ref), nil
	}
	if !bytes.HasPrefix(data, []byte("PK")) {
		return fmt.Sprintf("error: the stored file of %s is not a .docx (an old .doc file, for example), so it cannot be edited in place. Make a new document from its text with create_word (source %q), or save an edited copy with save_doc.", e.ref, e.ref), nil
	}
	op, msg := docxOp(e.op)
	if msg != "" {
		return msg, nil
	}
	out, what, err := docxedit.Edit(data, op)
	if err != nil {
		return "error: " + err.Error() + ". Nothing was changed.", nil
	}
	text, err := a.docxText(ctx, e.path, out)
	if err != nil {
		return "error: the edited file could not be read back (" + err.Error() + "). Nothing was changed.", nil
	}
	chunks, warn, err := a.commitDocEdit(ctx, e, text, out, nil)
	if err != nil {
		return "", err
	}
	if warn != "" {
		return "error: " + warn, nil
	}
	return fmt.Sprintf("Edited Word document %s (%q): %s%s. The original formatting was kept; the file is now %d characters of text in %d sections, re-indexed for search_docs.", e.ref, e.doc.Title, what, e.from, utf8.RuneCountInString(text), chunks), nil
}
