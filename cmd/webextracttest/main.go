// Command webextracttest exercises the web-extractor skill's JS end-to-end
// against a fake page (no network): it runs the documented setup (save recipe +
// parser), takes a baseline, then mutates the page and re-checks — asserting the
// repeat check finds the change using only the shipped JS, dom, and storage.
//
//	go run -tags sqlite_fts5 ./cmd/webextracttest
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ivoras/harlequin/internal/server/jsrun"
	"github.com/ivoras/harlequin/internal/server/sandboxfs"
)

type fetcher struct{ body *string }

func (f fetcher) FetchRaw(_ context.Context, url string) (jsrun.FetchResult, error) {
	return jsrun.FetchResult{Status: 200, Body: []byte(*f.body), FinalURL: url, ContentType: "text/html"}, nil
}

// page renders a calls-list page resembling the watched FZOEU structure.
func page(items []string) string {
	var b strings.Builder
	b.WriteString(`<html><head><title>Public calls</title></head><body><div id="main"><ul class="calls">`)
	for _, it := range items {
		b.WriteString(`<li class="call"><a href="#">` + it + `</a></li>`)
	}
	b.WriteString(`</ul></div></body></html>`)
	return b.String()
}

func main() {
	libDir := "skills/web-extractor/lib"
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(libDir, name))
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: read %s: %v (run from repo root)\n", name, err)
			os.Exit(1)
		}
		return string(b)
	}

	storeDir, _ := os.MkdirTemp("", "webx-store")
	tmpDir, _ := os.MkdirTemp("", "webx-tmp")
	defer os.RemoveAll(storeDir)
	defer os.RemoveAll(tmpDir)
	store := sandboxfs.New(storeDir, sandboxfs.Options{})
	tmp := sandboxfs.New(tmpDir, sandboxfs.Options{})

	body := page([]string{"Solar subsidy 2024", "EV charger grant"})
	runner := jsrun.New(jsrun.Options{Fetcher: fetcher{&body}})

	resolve := func(uri string) (string, error) {
		switch {
		case strings.HasPrefix(uri, "skill://web-extractor/lib/"):
			return read(strings.TrimPrefix(uri, "skill://web-extractor/lib/")), nil
		case strings.HasPrefix(uri, "storage://"):
			b, err := store.Read(strings.TrimPrefix(uri, "storage://"))
			return string(b), err
		case strings.HasPrefix(uri, "tmp://"):
			b, err := tmp.Read(strings.TrimPrefix(uri, "tmp://"))
			return string(b), err
		}
		return "", fmt.Errorf("unknown uri %s", uri)
	}
	rc := jsrun.RunContext{Ctx: context.Background(), Tmp: tmp, Storage: store, Resolve: resolve}

	run := func(label, code string) string {
		res, err := runner.Run(code, rc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s: %v\noutput: %s\n", label, err, res.Output)
			os.Exit(1)
		}
		fmt.Printf("[%s]\n%s\n", label, strings.TrimRight(res.Output, "\n"))
		return res.Output
	}
	runScript := func(label, uri string) string {
		code, err := resolve(uri)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: resolve %s: %v\n", uri, err)
			os.Exit(1)
		}
		return run(label, code)
	}
	expect := func(label, out, want string) {
		if !strings.Contains(out, want) {
			fmt.Fprintf(os.Stderr, "FAIL: %s: output missing %q\n", label, want)
			os.Exit(1)
		}
	}

	// Phase 1 setup: save recipe + the 2-line parser (exactly as SKILL.md instructs).
	run("setup", `
storage.write("fzoeu/recipe.json", JSON.stringify({url:"https://fzoeu.test/", selector:"ul.calls li a", label:"FZOEU calls", lastSeen:[], lastChecked:""}));
storage.write("fzoeu/parser.js", 'include("skill://web-extractor/lib/extract.js");\ncheckWatch("fzoeu");\n');
println("setup done");
`)

	// Baseline (first check vs empty state lists all current items as added).
	out := runScript("baseline", "storage://fzoeu/parser.js")
	expect("baseline", out, "CHANGED")
	expect("baseline", out, "+ Solar subsidy 2024")
	expect("baseline", out, "+ EV charger grant")

	// Re-check with no change.
	out = runScript("recheck-unchanged", "storage://fzoeu/parser.js")
	expect("recheck-unchanged", out, "No change")

	// A new call appears: the repeat check must detect it with no AI.
	body = page([]string{"Heat pump rebate 2025", "Solar subsidy 2024", "EV charger grant"})
	out = runScript("recheck-changed", "storage://fzoeu/parser.js")
	expect("recheck-changed", out, "CHANGED")
	expect("recheck-changed", out, "+ Heat pump rebate 2025")

	// And it persists the new state (next check is clean again).
	out = runScript("recheck-after-change", "storage://fzoeu/parser.js")
	expect("recheck-after-change", out, "No change")

	fmt.Println("\nPASS: web-extractor end-to-end workflow")
}
