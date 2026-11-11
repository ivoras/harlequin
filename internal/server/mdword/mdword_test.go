package mdword

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func docxXML(t *testing.T, data []byte) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			return string(b)
		}
	}
	t.Fatal("no document.xml")
	return ""
}

func TestDOCX(t *testing.T) {
	r, err := New("")
	if err != nil {
		t.Skip(err)
	}
	src := "# Naslov č\n\nTekst **bold**\n\n| A | B |\n|---|--:|\n| 1 | 2 |\n\n\\newpage\n\n## Dva\n\n- a\n- b\n"
	page, text, _, err := Page("T", src, false)
	if err != nil || !strings.Contains(text, "Naslov") {
		t.Fatal(err, text)
	}
	for _, land := range []bool{false, true} {
		data, err := r.DOCX(context.Background(), page, land)
		if err != nil {
			t.Fatal(err)
		}
		x := docxXML(t, data)
		for _, want := range []string{"Naslov č", "<w:tbl>", "Heading2", "w:numPr", `w:type="page"`} {
			if !strings.Contains(x, want) {
				t.Errorf("landscape=%v: missing %q", land, want)
			}
		}
		if got := strings.Contains(x, `w:orient="landscape" w:w="16838"`); got != land {
			t.Errorf("landscape=%v but A4 landscape pgSz=%v", land, got)
		}
	}
}

func TestPageRejectsEmpty(t *testing.T) {
	if _, _, _, err := Page("T", "  ", false); err == nil {
		t.Fatal("want error")
	}
}
