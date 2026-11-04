package agent

import "testing"

func TestMarkdownFetchFilename(t *testing.T) {
	pdf := []byte("%PDF-1.7\n...")
	html := []byte("<!doctype html><html><body>x</body></html>")
	cases := []struct {
		name, final, req, ct string
		body                 []byte
		want                 string
		ok                   bool
	}{
		{"pdf content type wins over url", "https://x/a.html", "https://x/a.html", "application/pdf", pdf, "document.pdf", true},
		{"content type params", "https://x/p", "https://x/p", "text/html; charset=utf-8", html, "document.html", true},
		{"docx", "https://x/d", "https://x/d", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil, "document.docx", true},
		{"octet-stream uses url ext", "https://x/report.PDF?dl=1", "https://x/dl", "application/octet-stream", pdf, "document.pdf", true},
		{"no content type sniffs pdf", "https://x/dl?id=3", "https://x/dl?id=3", "", pdf, "document.pdf", true},
		{"text/plain sniffs html", "https://x/p", "https://x/p", "text/plain", html, "document.html", true},
		{"json unsupported", "https://x/a.json", "https://x/a.json", "application/json", []byte("{}"), "", false},
		{"zip unsupported", "https://x/a.zip", "https://x/a.zip", "application/zip", []byte("PK\x03\x04"), "", false},
	}
	for _, c := range cases {
		got, ok := markdownFetchFilename(c.final, c.req, c.ct, c.body)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestMarkdownFetchSaveName(t *testing.T) {
	for in, want := range map[string]string{"base.md": "base.md", " tmp://base.md ": "base.md", "": "", "storage://x.md": "", "tmp://": ""} {
		if got := markdownFetchSaveName(in); got != want {
			t.Errorf("markdownFetchSaveName(%q) = %q, want %q", in, got, want)
		}
	}
}
