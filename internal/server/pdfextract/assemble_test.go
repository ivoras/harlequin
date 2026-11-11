package pdfextract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestPDF builds a minimal n-page PDF whose page i reads "<label> page i".
func testPDF(label string, n int) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(s string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), s)
	}
	b.WriteString("%PDF-1.4\n")
	kids := ""
	for i := 0; i < n; i++ {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, n))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i := 1; i <= n; i++ {
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 3+2*i))
		s := fmt.Sprintf("BT /F1 18 Tf 20 100 Td (%s page %d) Tj ET", label, i)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

// Pages are copied in the order asked for — extract, reorder, merge, and many
// outputs from one call — and the copies carry their text.
func TestAssemble(t *testing.T) {
	e, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	a, b := testPDF("A", 6), testPDF("B", 2)
	counts, err := e.PageCounts([][]byte{a, b})
	if err != nil || counts[0] != 6 || counts[1] != 2 {
		t.Fatalf("page counts %v (%v)", counts, err)
	}
	outs, err := e.Assemble([][]byte{a, b}, [][]PagePart{
		{{Source: 0, Pages: []int{2, 3, 4}}},                               // extract a range
		{{Source: 0, Pages: []int{5, 1, 3}}},                               // reorder
		{{Source: 1, Pages: []int{2}}, {Source: 0, Pages: []int{6, 6, 1}}}, // merge, repeat
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"A page 2|A page 3|A page 4", "A page 5|A page 1|A page 3", "B page 2|A page 6|A page 6|A page 1"}
	for i, pdf := range outs {
		pages, err := e.Pages(pdf)
		if err != nil {
			t.Fatalf("output %d: %v", i, err)
		}
		for j := range pages {
			pages[j] = strings.TrimSpace(pages[j])
		}
		if got := strings.Join(pages, "|"); got != want[i] {
			t.Errorf("output %d: %q, want %q", i, got, want[i])
		}
	}
	// The sources are untouched and still readable.
	if pages, err := e.Pages(a); err != nil || len(pages) != 6 {
		t.Errorf("source after assembling: %d pages, %v", len(pages), err)
	}
	if _, err := e.Assemble([][]byte{a}, [][]PagePart{{{Source: 0, Pages: nil}}}); err == nil {
		t.Error("an output without pages must be an error")
	}
	if _, err := e.Assemble([][]byte{[]byte("not a pdf")}, [][]PagePart{{{Source: 0, Pages: []int{1}}}}); err == nil {
		t.Error("a broken source must be an error")
	}
}
