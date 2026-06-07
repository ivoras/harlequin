package jsrun

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ivoras/harlequin/internal/server/sandboxfs"
)

type fakeFetcher struct{ body string }

func (f fakeFetcher) FetchRaw(_ context.Context, url string) (FetchResult, error) {
	return FetchResult{Status: 200, Body: []byte(f.body), FinalURL: url, ContentType: "text/html"}, nil
}

const pageHTML = `<html><body><ul class="calls">
<li class="call"><a href="/c/1">Solar subsidy</a></li>
<li class="call"><a href="/c/2">EV grant</a></li>
</ul></body></html>`

// The discovery→parser workflow in miniature: fetch a page and extract data with
// a CSS selector, no LLM involved.
func TestFetchAndDom(t *testing.T) {
	r := New(Options{Fetcher: fakeFetcher{body: pageHTML}})
	code := `
var resp = fetch("https://example.test/");
var h = dom.parse(resp.body);
var rows = dom.query(h, "li.call > a");
println(rows.length);
println(rows[0].text);
println(rows[1].attrs.href);
`
	res, err := r.Run(code, RunContext{})
	if err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, res.Output)
	}
	want := "2\nSolar subsidy\n/c/2\n"
	if res.Output != want {
		t.Fatalf("output = %q, want %q", res.Output, want)
	}
}

func TestDomGrep(t *testing.T) {
	r := New(Options{})
	code := `
var h = dom.parse(html);
var hits = dom.grep(h, "EV grant");
println(hits.length);
println(hits[0].path);
`
	res, err := r.Run(code, RunContext{Globals: map[string]any{"html": pageHTML}})
	if err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, res.Output)
	}
	lines := strings.Split(strings.TrimRight(res.Output, "\n"), "\n")
	if lines[0] != "1" {
		t.Fatalf("grep count = %q, want 1 (output %q)", lines[0], res.Output)
	}
	if !strings.Contains(lines[1], "nth-of-type") && !strings.Contains(lines[1], "#") {
		t.Fatalf("grep path looks wrong: %q", lines[1])
	}
}

func TestTmpStorageRoundTrip(t *testing.T) {
	store := sandboxfs.New(t.TempDir(), sandboxfs.Options{})
	tmp := sandboxfs.New(t.TempDir(), sandboxfs.Options{})
	code := `
storage.write("fzoeu/recipe.json", "{\"last\":\"abc\"}");
tmp.write("scratch.txt", "hi");
println(storage.read("fzoeu/recipe.json"));
println(storage.exists("fzoeu/recipe.json"));
println(tmp.read("scratch.txt"));
`
	r := New(Options{})
	out, runErr := r.Run(code, RunContext{Storage: store, Tmp: tmp})
	if runErr != nil {
		t.Fatalf("Run: %v\noutput: %s", runErr, out.Output)
	}
	want := "{\"last\":\"abc\"}\ntrue\nhi\n"
	if out.Output != want {
		t.Fatalf("output = %q, want %q", out.Output, want)
	}
}

func TestStorageNotAvailableErrors(t *testing.T) {
	r := New(Options{})
	_, err := r.Run(`storage.write("x", "y");`, RunContext{})
	if err == nil {
		t.Fatal("expected error writing to unconfigured storage")
	}
}

func TestIncludeDefinesGlobals(t *testing.T) {
	r := New(Options{})
	lib := `function add(a, b){ return a + b; }`
	rc := RunContext{Resolve: func(uri string) (string, error) {
		if uri == "skill://web-extractor/lib/extract.js" {
			return lib, nil
		}
		return "", fmt.Errorf("unknown uri %q", uri)
	}}
	code := `
include("skill://web-extractor/lib/extract.js");
println(add(2, 3));
println(load("skill://web-extractor/lib/extract.js").length > 0);
`
	res, err := r.Run(code, rc)
	if err != nil {
		t.Fatalf("Run: %v\noutput: %s", err, res.Output)
	}
	if res.Output != "5\ntrue\n" {
		t.Fatalf("output = %q", res.Output)
	}
}
