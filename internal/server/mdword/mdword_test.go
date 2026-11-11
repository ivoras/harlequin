package mdword

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
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
		if !strings.Contains(x, `w:left="1134" w:right="1134"`) || !strings.Contains(x, `w:top="1134"`) || !strings.Contains(x, `w:bottom="1134"`) {
			t.Errorf("landscape=%v: margins are not 2 cm all round", land)
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

// LibreOffice reads its own HTML dialect: macro-binding sd… attributes and
// document fields must not reach it, nor anything else active or external.
func TestHostileHTMLIsInert(t *testing.T) {
	src := `<body sdonload="Standard.Module1.Main"><h1>Dopis</h1>
<p sdonclick="Standard.Module1.Main" SDEVENT-OnClick="x" onclick="y()" style="background:url(file:///etc/passwd)">Tekst</p>
<sdfield type=DOCINFO subtype=CHANGE format=AUTHOR>autor</sdfield><sdfield type="datetime">datum</sdfield>
<script language="StarBasic">Sub Main: End Sub</script><iframe src="file:///etc/passwd"></iframe>
<td sdval="1" sdnum="1033;">1</td><img src="file:///etc/hostname"><a href="macro:///Standard.Module1.Main">m</a></body>`
	page, _, st, err := Page("T", src, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"sdon", "sdevent", "sdfield", "sdval", "sdnum", "onclick", "StarBasic", "<script", "<iframe", "file:///", "macro:", "autor", "datum"} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(bad)) {
			t.Errorf("page contains %q", bad)
		}
	}
	if !strings.Contains(page, "<h1>Dopis</h1>") || !strings.Contains(page, "Tekst") || st.Removed < 9 || st.ImagesDropped != 1 {
		t.Errorf("stats %+v, page %q", st, page)
	}
	r, err := New("")
	if err != nil {
		t.Skip(err)
	}
	data, err := r.DOCX(context.Background(), page, false)
	if err != nil {
		t.Fatal(err)
	}
	if x := docxXML(t, data); strings.Contains(x, "w:instrText") || strings.Contains(x, "w:fldSimple") || !strings.Contains(x, "Dopis") {
		t.Error("the DOCX carries a field, or lost its text")
	}
}

func TestNewRefusesLibreOfficeOutsideUsr(t *testing.T) {
	dir := t.TempDir()
	fake := dir + "/soffice"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(fake); err == nil || !strings.Contains(err.Error(), "outside /usr") {
		t.Fatalf("want an 'outside /usr' error, got %v", err)
	}
}
