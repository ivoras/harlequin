package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivoras/harlequin/internal/server/mdpdf"
	"github.com/ivoras/harlequin/internal/server/mdword"
	"github.com/ivoras/harlequin/internal/server/mdxlsx"
)

// Documents made by create_excel/create_pdf/create_word keep what they were
// generated from in a sidecar next to the stored file, so edit_doc can edit that
// source and regenerate the file.
const (
	sidecarExt = ".src.json"
	prevExt    = ".prev" // one level of undo, for the stored file and the sidecar

	genExcel = "excel"
	genPDF   = "pdf"
	genWord  = "word"

	pdfNotSetUp  = "error: PDF creation is not set up on this server (an administrator has to install headless Chrome; see the README). Tell the user, and offer to save the text as a Markdown document with save_doc instead."
	wordNotSetUp = "error: Word document creation is not set up on this server (an administrator has to install LibreOffice and bubblewrap; see the README). Tell the user, and offer to save the text as a Markdown document with save_doc instead."
)

// genSource is the sidecar's content.
type genSource struct {
	Kind      string `json:"kind"`   // excel | pdf | word
	Format    string `json:"format"` // markdown | html
	Landscape bool   `json:"landscape"`
	Source    string `json:"source"` // exactly as given
}

// genOut is what a converter produces from a genSource.
type genOut struct {
	data   []byte // the file; nil when not rendered
	text   string // the searchable text stored in the index
	detail string // result-message fragment after the title, e.g. ": 2 page(s)"
	notes  string // result-message notes (dropped images, ...)
}

// convertError marks a source the converter rejected (as opposed to a render
// failure), so callers can say how to fix the input.
type convertError struct{ error }

// notSetUp is the tool result when the renderer for kind is missing, else "".
func (a *Agent) notSetUp(kind string) string {
	switch {
	case kind == genPDF && a.PDF == nil:
		return pdfNotSetUp
	case kind == genWord && a.Word == nil:
		return wordNotSetUp
	}
	return ""
}

// generate turns source text into the file and the searchable text; create and
// edit share it so a regenerated file is exactly what a fresh create would give.
// render false stops short of the (slow) renderer: only text and notes are made.
func (a *Agent) generate(ctx context.Context, title string, s genSource, render bool) (genOut, error) {
	var out genOut
	html := s.Format == "html"
	switch s.Kind {
	case genExcel:
		xlsx, st, err := mdxlsx.Convert(s.Source)
		if err != nil {
			return out, convertError{err}
		}
		out.data, out.text = xlsx, strings.TrimSpace(s.Source) // the Markdown is what docling would extract
		out.detail = fmt.Sprintf(": %d sheet(s), %d table(s), %d row(s)", st.Sheets, st.Tables, st.Rows)
		if st.Ignored > 0 {
			out.notes = fmt.Sprintf(" Note: %d non-table line(s) were left out (only headings and tables go into the workbook).", st.Ignored)
		}
	case genPDF:
		page, text, st, err := mdpdf.Page(strings.TrimSuffix(title, ".pdf"), s.Source, html, s.Landscape)
		if err != nil {
			return out, convertError{err}
		}
		out.text = text
		if render {
			if out.data, err = a.PDF.PDF(ctx, page); err != nil {
				return out, fmt.Errorf("could not create the PDF: %w", err)
			}
			if n := mdpdf.Pages(out.data); n > 0 {
				out.detail = fmt.Sprintf(": %d page(s)", n)
			}
		}
		if st.ImagesDropped > 0 {
			out.notes += fmt.Sprintf(" Note: %d image(s) were left out (only inline data: images can be printed); tell the user.", st.ImagesDropped)
		}
		if st.Removed > 0 {
			out.notes += fmt.Sprintf(" Note: %d script, form, embedded or external item(s) were removed from the HTML (a PDF is static and self-contained).", st.Removed)
		}
	case genWord:
		page, text, st, err := mdword.Page(strings.TrimSuffix(title, ".docx"), s.Source, html)
		if err != nil {
			return out, convertError{err}
		}
		out.text = text
		if render {
			if out.data, err = a.Word.DOCX(ctx, page, s.Landscape); err != nil {
				return out, fmt.Errorf("could not create the Word document: %w", err)
			}
		}
		if st.ImagesDropped > 0 {
			out.notes += fmt.Sprintf(" Note: %d image(s) were left out (only inline data: images are embedded); tell the user.", st.ImagesDropped)
		}
		if st.Removed > 0 {
			out.notes += fmt.Sprintf(" Note: %d script, form, embedded or external item(s) were removed from the HTML.", st.Removed)
		}
	default:
		return out, fmt.Errorf("unknown generated-file kind %q", s.Kind)
	}
	return out, nil
}

func formatName(isHTML bool) string {
	if isHTML {
		return "html"
	}
	return "markdown"
}

// writeSidecar records what the stored file was generated from. Best effort at
// create time: without it the document is still a normal, downloadable file.
func writeSidecar(dir, stored string, s genSource) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, stored+sidecarExt), data, 0o600)
}

func readSidecar(data []byte) (genSource, error) {
	var s genSource
	if err := json.Unmarshal(data, &s); err != nil || s.Kind == "" {
		return s, fmt.Errorf("its generation record is unreadable")
	}
	return s, nil
}
