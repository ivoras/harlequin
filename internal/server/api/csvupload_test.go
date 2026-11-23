package api

import (
	"context"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// CSV/TSV uploads are spreadsheets: typed as such, and accepted when they are
// not UTF-8 (legacy accounting exports), their text decoded for the index.
func TestCSVUploads(t *testing.T) {
	cp, _ := charmap.Windows1250.NewEncoder().String("Konto;Naziv\n01270000;Licenca čžš\n")
	for _, c := range []struct{ name, data, mime, text string }{
		{"izvoz.csv", cp, "text/csv", "Konto;Naziv\n01270000;Licenca čžš\n"},
		{"izvoz.TSV", "a\tb\n", "text/tab-separated-values", "a\tb\n"},
		{"notes.txt", "hello", "text/plain", "hello"},
	} {
		in := uploadInput{data: []byte(c.data), filename: c.name, title: c.name, scope: "personal"}
		req, status, msg := (&Server{}).extractDocument(context.Background(), in)
		if status != 0 || req.Mime != c.mime || req.Content != c.text {
			t.Errorf("%s: status %d %q, mime %q, content %q", c.name, status, msg, req.Mime, req.Content)
		}
	}
}
