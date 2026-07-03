package docxextract

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// makeDocx builds a minimal DOCX: a zip with word/document.xml.
func makeDocx(t *testing.T, documentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(documentXML)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const sampleXML = `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Article 1.</w:t></w:r><w:r><w:t xml:space="preserve"> Scope of the rules.</w:t></w:r></w:p>
    <w:p><w:r><w:t>Second</w:t></w:r><w:r><w:tab/><w:t>paragraph</w:t></w:r><w:r><w:br/><w:t>with a break.</w:t></w:r></w:p>
    <w:p></w:p>
    <w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell text</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
  </w:body>
</w:document>`

func TestText(t *testing.T) {
	got, err := Text(makeDocx(t, sampleXML))
	if err != nil {
		t.Fatal(err)
	}
	want := "Article 1. Scope of the rules.\n\nSecond\tparagraph\nwith a break.\n\ncell text"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTextRejectsNonZip(t *testing.T) {
	if _, err := Text([]byte("%PDF-1.7 not a docx")); err == nil || !strings.Contains(err.Error(), "zip") {
		t.Fatalf("want zip error, got %v", err)
	}
}

func TestTextRejectsZipWithoutDocument(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("other.txt")
	w.Write([]byte("hi"))
	zw.Close()
	if _, err := Text(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "word/document.xml") {
		t.Fatalf("want missing-document error, got %v", err)
	}
}
