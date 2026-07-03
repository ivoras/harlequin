// Package docxextract extracts plain text from DOCX bytes with the standard
// library only: a .docx file is a zip archive whose main text lives in
// word/document.xml as <w:p> paragraphs of <w:r> runs of <w:t> text nodes.
// It is the fallback used when no Docling converter is configured — good for
// clean text, with no layout, table-structure or image handling.
package docxextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// maxDocumentXML caps the decompressed size of word/document.xml (a zip-bomb
// guard; real documents are nowhere near this).
const maxDocumentXML = 64 << 20

// Text extracts the document's paragraphs, joined with blank lines.
func Text(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docxextract: not a zip archive: %w", err)
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", fmt.Errorf("docxextract: no word/document.xml (not a DOCX file)")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", fmt.Errorf("docxextract: open document.xml: %w", err)
	}
	defer rc.Close()
	text, err := parseDocumentXML(io.LimitReader(rc, maxDocumentXML))
	if err != nil {
		return "", fmt.Errorf("docxextract: parse document.xml: %w", err)
	}
	return text, nil
}

// parseDocumentXML streams WordprocessingML, collecting <w:t> text, mapping
// <w:tab> and <w:br> to whitespace, and ending a paragraph at each </w:p>.
func parseDocumentXML(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var out []string
	var para strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				para.WriteString("\t")
			case "br", "cr":
				para.WriteString("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if s := strings.TrimSpace(para.String()); s != "" {
					out = append(out, s)
				}
				para.Reset()
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	if s := strings.TrimSpace(para.String()); s != "" {
		out = append(out, s)
	}
	return strings.Join(out, "\n\n"), nil
}
