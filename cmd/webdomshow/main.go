// Command webdomshow renders, for a local HTML file, the result WebFetchDOM would
// return for a given grep/selector, and prints its byte size against the tool's
// result cap. It uses the dom package directly (no network, no LLM), so it is the
// quickest way to inspect what the model would see for a page.
//
//	go run -tags sqlite_fts5 ./cmd/webdomshow [-grep S | -selector S] [-context N] <file.html>
//	(flags must precede the file argument)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ivoras/harlequin/internal/server/dom"
)

const resultCap = 20000

func main() {
	grep := flag.String("grep", "", "substring to grep in the flattened page (with ±context lines)")
	selector := flag.String("selector", "", "comma-separated tag/class selectors; returns parent/siblings/children YAML")
	contextN := flag.Int("context", 3, "context lines around each grep match")
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
		corpus := append([]string{"# Links on the website"}, d.LinkLines()...)
		report("grep "+fmt.Sprintf("%q", *grep)+" (flattened+links, ±"+fmt.Sprint(*contextN)+")", d.GrepFlatten(*grep, *contextN, corpus))
	case *selector != "":
		fams, ferr := d.SelectFamily(*selector, 0, 3, 3)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, ferr)
			os.Exit(1)
		}
		report("selector "+fmt.Sprintf("%q", *selector)+" (family YAML)", dom.FamiliesYAML(fams))
	default:
		b, _ := json.Marshal(d.RepeatingGroups(3, 15, 160))
		report("candidate lists", string(b))
	}

	// Mirror the tool: always append the page's links.
	links := d.LinkLines()
	fmt.Printf("\n\n# Links on the website (%d unique)\n", len(links))
	for _, l := range links {
		fmt.Println("- " + l)
	}
}

func report(label, s string) {
	fmt.Printf("== %s ==\n%d bytes (fits %d-byte cap: %v)\n\n", label, len(s), resultCap, len(s) <= resultCap)
	if len(s) > resultCap {
		s = s[:resultCap] + "\n…[truncated]"
	}
	fmt.Println(s)
}
