// Command webdomshow renders, for a local HTML file, the exact result
// WebFetchDOM would return for a given grep/selector — plus a terser
// line-oriented projection — and prints the byte size of each. It uses the dom
// package directly (no network, no LLM), so it is the quickest way to inspect
// what the model would see for a page and to compare result encodings against
// the tool's result-size cap.
//
//	go run -tags sqlite_fts5 ./cmd/webdomshow <file.html> [-grep S | -selector S] [-context N]
//	(flags must precede the file argument)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ivoras/harlequin/internal/server/dom"
)

// resultCap mirrors agent.webFetchDOMResultCap.
const resultCap = 15000

func main() {
	grep := flag.String("grep", "", "grep this text (with context), as WebFetchDOM does")
	selector := flag.String("selector", "", "CSS selector to extract as records")
	contextN := flag.Int("context", 3, "context elements around each grep match")
	flag.Parse()
	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: webdomshow [-grep S | -selector S] [-context N] <file.html>")
		os.Exit(2)
	}
	page, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	d, err := dom.Parse(page)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	switch {
	case *grep != "":
		// Mirror the tool: the match keeps its path; context nodes are pathless.
		mcs, _ := d.GrepContext(*grep, dom.GrepOptions{IgnoreCase: true, Attrs: true, MaxMatches: 20},
			dom.ContextOptions{Siblings: *contextN, Ancestors: *contextN, TextChars: 110})
		report("grep "+strconv.Quote(*grep)+" + context", jsonForm(mcs), grepLines(mcs))
	case *selector != "":
		recs, _ := d.Records(*selector, 40, 110)
		for i := range recs {
			recs[i].Path = "" // mirror the tool: path dropped, one link kept
			if len(recs[i].Links) > 1 {
				recs[i].Links = recs[i].Links[:1]
			}
		}
		report("selector "+strconv.Quote(*selector)+" records", jsonForm(recs), recordLines(recs))
	default:
		report("candidate lists", jsonForm(d.RepeatingGroups(3, 15, 160)), nil)
	}
}

func jsonForm(v any) string { b, _ := json.Marshal(v); return string(b) }

// recordLines renders one record per line: its text (then first link, if any).
func recordLines(recs []dom.Record) []string {
	var out []string
	for _, r := range recs {
		line := r.Text
		if len(r.Links) > 0 {
			line += "  <" + r.Links[0] + ">"
		}
		out = append(out, line)
	}
	return out
}

// grepLines flattens each match to "<match text> | <outermost ancestor text>".
func grepLines(mcs []dom.MatchContext) []string {
	var out []string
	for _, m := range mcs {
		ctx := ""
		if len(m.Ancestors) > 0 {
			ctx = m.Ancestors[len(m.Ancestors)-1].Text
		}
		out = append(out, m.Match.Text+" | "+ctx)
	}
	return out
}

// marshalCapped mirrors agent.marshalCapped (the tool's result-size guard).
func marshalCapped(s string) string {
	if len(s) > resultCap {
		return s[:resultCap] + "\n…[truncated — use a tighter grep/selector or run_js against the saved handle]"
	}
	return s
}

func report(label, jsonStr string, lines []string) {
	fmt.Printf("== %s ==\n", label)
	fmt.Printf("JSON: %d bytes (fits %d-byte cap: %v)\n", len(jsonStr), resultCap, len(jsonStr) <= resultCap)
	if lines != nil {
		lineStr := strings.Join(lines, "\n")
		fmt.Printf("LINES: %d bytes (fits: %v)\n", len(lineStr), len(lineStr) <= resultCap)
	}
	fmt.Printf("\n--- EXACT tool result (JSON, capped to %dB as the agent sees it) ---\n%s\n", resultCap, marshalCapped(jsonStr))
	if lines != nil {
		fmt.Printf("\n--- line-form projection (full) ---\n%s\n", strings.Join(lines, "\n"))
	}
}
