package mdpdf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = "# Izvještaj — šđčćž\n\nTekst s **podebljanim**.\n\n" +
	"| Zaposlenik | Neto |\n|---|---:|\n| Đurđica Šimić | 1.184,35 |\n\n" +
	"\\newpage\n\n## Druga stranica\n\nKraj.\n"

// The page is built to be safe to hand to a browser: a CSP, no raw HTML or
// script from the source, and no image that would have to be fetched.
func TestMarkdownPageIsInert(t *testing.T) {
	src := sample + "\n<script>alert(1)</script>\n\n<img src=\"file:///etc/hostname\">\n\n" +
		"![logo](http://example.com/x.png) ![](file:///etc/passwd) ![dot](data:image/png;base64,iVBORw0KGgo=)\n\n" +
		"[link](javascript:alert(1))\n\n```\n\\newpage\n```\n"
	page, text, st, err := Page(`Plaće <2026>`, src, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "alert(1)</script>", "file:///", "http://example.com", "javascript:"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	for _, want := range []string{
		`<head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'`, "<title>Plaće &lt;2026&gt;</title>",
		"size:A4 portrait", "<em>[image: logo]</em>", "<em>[image]</em>", `src="data:image/png;base64,iVBORw0KGgo="`,
		"<table>", "text-align:right", `<div class="pagebreak"></div>`, "<code>\\newpage\n</code>",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if st.ImagesDropped != 2 || strings.Count(page, `class="pagebreak"`) != 1 || text != strings.TrimSpace(src) {
		t.Errorf("dropped %d images, %d page breaks", st.ImagesDropped, strings.Count(page, `class="pagebreak"`))
	}
	if p, _, _, _ := Page("t", "x", false, true); !strings.Contains(p, "size:A4 landscape") {
		t.Error("landscape not applied")
	}
	if _, _, _, err := Page("t", " \n", false, false); err == nil {
		t.Error("an empty document must be refused")
	}
}

// HTML input keeps its layout (its own CSS, inline images, SVG) and loses
// everything that runs, loads, embeds or submits, however it is spelled.
func TestHTMLPageIsSanitized(t *testing.T) {
	src := `<!DOCTYPE html><html><head>
<meta http-equiv="Content-Security-Policy" content="default-src *">
<meta http-equiv="refresh" content="0;url=http://evil.example/">
<base href="http://evil.example/"><link rel="stylesheet" href="http://evil.example/a.css">
<title>ignored</title>
<style>@import url("http://evil.example/b.css"); .red{color:#c00;background:url(http://evil.example/t.png)} .ok{background:url(data:image/png;base64,AAAA)} @font-face{src:url(file:///etc/passwd)}</style>
</head><body onload="steal()" class="doc">
<h1 class="red" style="background:URL( 'file:///etc/shadow' );margin:0">Naslov šđčćž</h1>
<p onclick="x()">Tekst <a href="https://example.com/a">veza</a> <a href=" java&#9;script:alert(1)">zlo</a> <a href="file:///etc/passwd">lokalno</a> <a href="#dno">dolje</a></p>
<script>document.write("JS-RAN")</script><ScRiPt src="http://evil.example/x.js"></ScRiPt>
<img src="http://evil.example/p.png" alt="logo"><img src="data:image/png;base64,iVBORw0KGgo=" onerror="x()"><img src="data:image/svg+xml,<svg onload=x()>">
<iframe src="file:///etc/passwd"></iframe><object data="x.swf"></object><embed src="x"><form action="http://evil.example/"><input name="a"><button>Go</button></form>
<svg width="10" height="10"><script>alert(2)</script><circle r="5" onmouseover="x()"/><use href="http://evil.example/s.svg#a"/><use xlink:href="#local"/><image href="file:///usr/share/x.png"/><style>&lt;/style&gt;&lt;script&gt;alert(3)&lt;/script&gt;</style></svg>
<table><tr><td style="text-align:right">1,50</td></tr></table>
<div class="pagebreak"></div><p id="dno">Kraj</p>
<!-- comment <script>alert(4)</script> -->
</body></html>`
	page, text, st, err := Page("Dopis", src, true, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"<script", "<ScRiPt", "evil.example", "file:///", "steal()", "onclick", "onload", "onerror", "onmouseover", "JS-RAN",
		"javascript:", "java\tscript", "<iframe", "<object", "<embed", "<form", "<input", "<button", "<base", "<link",
		"http-equiv=\"refresh\"", "default-src *", "@import", "alert(1)", "alert(2)", "alert(4)", "data:image/svg", "<title>ignored", "<!--",
	} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	for _, want := range []string{
		`<head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'`, "<title>Dopis</title>",
		".red{color:#c00;background:none}", ".ok{background:url(data:image/png;base64,AAAA)}", "@font-face{src:none}",
		`<h1 class="red" style="background:none;margin:0">Naslov šđčćž</h1>`, `<a href="https://example.com/a">veza</a>`,
		`<a>zlo</a>`, `<a>lokalno</a>`, `<a href="#dno">dolje</a>`, "<em>[image: logo]</em>", `<img src="data:image/png;base64,iVBORw0KGgo="/>`,
		`<circle r="5"></circle>`, `xlink:href="#local"`, `\3c /style>\3c script>alert(3)`, `<td style="text-align:right">1,50</td>`, `<div class="pagebreak"></div>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Count(page, "Content-Security-Policy") != 1 || strings.Count(page, "</style>") != 2 {
		t.Errorf("page has %d CSPs and %d </style>", strings.Count(page, "Content-Security-Policy"), strings.Count(page, "</style>"))
	}
	if st.ImagesDropped != 2 || st.Removed < 15 {
		t.Errorf("stats %+v", st)
	}
	// The searchable text is Markdown, not markup.
	if !strings.Contains(text, "# Naslov šđčćž") || !strings.Contains(text, "[veza](https://example.com/a)") || strings.Contains(text, "<p") {
		t.Errorf("text: %q", text)
	}
	// A bare fragment is a document too; markup with nothing to print is refused.
	if p, _, _, err := Page("t", "<h2>Samo naslov</h2><style>h2{color:red}</style>", true, false); err != nil || !strings.Contains(p, "<h2>Samo naslov</h2>") || !strings.Contains(p, "h2{color:red}") {
		t.Errorf("fragment: %v", err)
	}
	if _, _, _, err := Page("t", "<script>alert(1)</script><div></div>", true, false); err == nil {
		t.Error("HTML with no visible text must be refused")
	}
}

// End to end through the sandboxed Chrome; skipped where it is not installed.
func TestRenderPDF(t *testing.T) {
	r, err := New("")
	if err != nil {
		t.Skip(err)
	}
	page, _, _, err := Page("Izvještaj", sample, false, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HQ_SECRET", "s3cret") // the server's environment must not reach Chrome
	pdf, err := r.PDF(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if n := Pages(pdf); n != 2 {
		t.Errorf("%d pages, want 2 (\\newpage)", n)
	}
	if out := os.Getenv("MDPDF_KEEP"); out != "" {
		_ = os.WriteFile(filepath.Join(out, "sample.pdf"), pdf, 0o644)
	}

	// The sandbox has no network and none of the host's files: a page that asks
	// for them (CSP stripped, to test the sandbox alone) still prints, and the
	// scratch directory is gone afterwards.
	hostile := strings.Replace(page, `<meta http-equiv="Content-Security-Policy" content="`+csp+`">`, "", 1)
	hostile = strings.Replace(hostile, "</body>", `<img src="http://192.0.2.1/x.png"><iframe src="file:///etc/passwd"></iframe><iframe src="file:///home"></iframe></body>`, 1)
	if _, err := r.PDF(context.Background(), hostile); err != nil {
		t.Errorf("hostile page: %v", err)
	}

	// HTML input with its own layout prints too.
	styled, _, _, err := Page("Dopis", `<style>h1{color:#c00} .two{columns:2}</style><h1>Dopis</h1><div class="two"><p>Lijevo</p><p>Desno</p></div><div class="pagebreak"></div><p>Druga</p>`, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if pdf, err = r.PDF(context.Background(), styled); err != nil || Pages(pdf) != 2 {
		t.Errorf("styled HTML: %d pages, %v", Pages(pdf), err)
	}
	if out := os.Getenv("MDPDF_KEEP"); out != "" {
		_ = os.WriteFile(filepath.Join(out, "styled.pdf"), pdf, 0o644)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "hq-pdf-*")); len(left) != 0 {
		t.Errorf("scratch directories left behind: %v", left)
	}
}
