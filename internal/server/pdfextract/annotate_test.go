package pdfextract

import (
	"strings"
	"testing"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/requests"
)

// annots reads back (subtype, Contents) of every annotation on each page.
func annots(t *testing.T, e *Extractor, data []byte) [][][2]string {
	t.Helper()
	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, _ := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	out := make([][][2]string, pc.PageCount)
	for i := range pc.PageCount {
		pg := requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}
		n, _ := inst.FPDFPage_GetAnnotCount(&requests.FPDFPage_GetAnnotCount{Page: pg})
		for j := range n.Count {
			a, err := inst.FPDFPage_GetAnnot(&requests.FPDFPage_GetAnnot{Page: pg, Index: j})
			if err != nil {
				t.Fatal(err)
			}
			st, _ := inst.FPDFAnnot_GetSubtype(&requests.FPDFAnnot_GetSubtype{Annotation: a.Annotation})
			c, _ := inst.FPDFAnnot_GetStringValue(&requests.FPDFAnnot_GetStringValue{Annotation: a.Annotation, Key: "Contents"})
			out[i] = append(out[i], [2]string{subtypeName(st.Subtype), c.Value})
			inst.FPDFPage_CloseAnnot(&requests.FPDFPage_CloseAnnot{Annotation: a.Annotation})
		}
	}
	return out
}

func subtypeName(s enums.FPDF_ANNOTATION_SUBTYPE) string {
	for k, v := range markSubtypes {
		if v == s {
			return k
		}
	}
	return "other"
}

func TestAnnotate(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	src := testPDF("Alpha", 3) // pages read "Alpha page 1".."Alpha page 3"
	out, res, err := e.Annotate(src, []Mark{
		{Kind: "highlight", Text: "alpha page 2", Comment: "check this"},       // case-insensitive, one page
		{Kind: "underline", Text: "Alpha", Page: 3},                            // restricted to a page
		{Kind: "strikeout", Text: "page", Nth: 2},                              // second match overall
		{Kind: "note", Page: 1, X: 50, Y: 50, Comment: "hello", Author: "Ana"}, // positioned note
		{Kind: "box", Text: "no such phrase"},                                  // not found
		{Kind: "highlight", Text: "Alpha", Color: "chartreuse"},                // bad color
		{Kind: "scribble", Text: "Alpha"},                                      // bad kind
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 1, 1, 1, 0, 0, 0} {
		if res[i].Applied != want {
			t.Errorf("mark %d applied %d (%q), want %d", i, res[i].Applied, res[i].Problem, want)
		}
	}
	for _, i := range []int{4, 5, 6} {
		if res[i].Problem == "" {
			t.Errorf("mark %d: no problem reported", i)
		}
	}
	got := annots(t, e, out)
	if len(got) != 3 || len(got[0]) != 1 || len(got[1]) != 2 || len(got[2]) != 1 {
		t.Fatalf("annotations per page = %v", got)
	}
	if got[1][0] != [2]string{"highlight", "check this"} && got[1][1] != [2]string{"highlight", "check this"} {
		t.Errorf("page 2 annotations %v lack the commented highlight", got[1])
	}
	pages, err := e.Pages(out)
	if err != nil || !strings.Contains(pages[1], "Alpha page 2") {
		t.Fatalf("text changed: %q (%v)", pages, err)
	}
	pg, _ := e.Pages(src)
	if len(pg) != 3 {
		t.Fatal("source changed")
	}
}
