// Command domprobe fetches a URL and prints the dom RepeatingGroups candidates —
// the same "candidate lists" WebFetchDOM surfaces. Lets us verify list discovery
// on a live page without the LLM.
//
//	go run ./cmd/domprobe "<url>"
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ivoras/harlequin/internal/server/dom"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: domprobe <url>")
		os.Exit(2)
	}
	raw, err := webfetch.New(webfetch.Options{}).FetchRaw(context.Background(), os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
	d, err := dom.Parse(raw.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	fmt.Printf("%d bytes from %s\n\nall groups (minCount=2):\n", len(raw.Body), raw.FinalURL)
	for _, g := range d.RepeatingGroups(2, 60, 100) {
		fmt.Printf("  %3d  %-52s  %s\n", g.Count, g.Selector, g.Sample)
	}
	for _, sel := range os.Args[2:] {
		n, _ := d.Query(sel, 0)
		fmt.Printf("\nQuery %q -> %d nodes\n", sel, len(n))
		for i := 0; i < len(n) && i < 3; i++ {
			fmt.Printf("   [%s] %.90s\n", n[i].Path, n[i].Text)
		}
	}
}
