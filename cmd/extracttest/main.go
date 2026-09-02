// Command extracttest compares document converters — docling-serve and xberg —
// on real files, so the choice between them is made on evidence rather than on
// their README claims. For each input file it runs every configured converter
// and reports, per converter: wall time, extracted length, the page mapping it
// produced, how much Markdown structure survived (headings, tables, lists), and
// whether the two agree on the text. It also validates the page mapping against
// the text it belongs to, which is the part most likely to silently break.
//
// No LLM and no database: this is pure extraction. It reads server.yaml only to
// pick up the configured converter endpoints; -docling/-xberg override them.
//
//	go run ./cmd/extracttest [-config server.yaml] [flags] <file.pdf> [file2.docx …]
//
// Useful flags:
//
//	-docling URL   docling-serve base URL (default: from config)
//	-xberg URL     xberg base URL (default: from config)
//	-fast          fast mode: no OCR, no layout models (default true)
//	-both          run each converter twice, fast and full-quality
//	-dump DIR      write each conversion's text to DIR for eyeballing/diffing
//	-pages N       print the first N runes of each mapped page
//	-builtin=false drop the built-in pure-Go extractor baseline row
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/docling"
	"github.com/ivoras/harlequin/internal/server/pdfextract"
	"github.com/ivoras/harlequin/internal/server/xberg"
)

// converter is the shared surface of the two clients (api.DocConverter, minus
// the dependency on the api package).
type converter interface {
	Name() string
	Healthy(ctx context.Context) bool
	Convert(ctx context.Context, filename string, data []byte) (string, error)
	ConvertPaged(ctx context.Context, filename string, data []byte) (string, []int, error)
}

// builtinConverter wraps the pure-Go PDFium extractor the server already falls
// back to, so every comparison shows what the converters are actually buying.
// It recovers no Markdown structure at all — no headings, no tables — but it
// reproduces the PDF's text layer verbatim, which means enumerators like "(b)"
// survive exactly as written. On a corpus where the enumerator is part of the
// citation that can matter more than structure, and it is the only option here
// with no service to run and no model to load.
type builtinConverter struct{ ex *pdfextract.Extractor }

func (b builtinConverter) Name() string                 { return "builtin" }
func (b builtinConverter) Healthy(context.Context) bool { return b.ex != nil }

func (b builtinConverter) Convert(_ context.Context, _ string, data []byte) (string, error) {
	text, _, err := b.ex.Text(data)
	return text, err
}

// ConvertPaged mirrors exactly what handlers.go does with this extractor, so the
// harness measures the server's real fallback rather than an idealised version.
func (b builtinConverter) ConvertPaged(_ context.Context, _ string, data []byte) (string, []int, error) {
	pages, err := b.ex.Pages(data)
	if err != nil {
		return "", nil, err
	}
	var sb strings.Builder
	var starts []int
	runeOff := 0
	for i, pg := range pages {
		if i > 0 {
			sb.WriteString("\n\n")
			runeOff += 2
		}
		starts = append(starts, runeOff)
		sb.WriteString(pg)
		runeOff += utf8.RuneCountInString(pg)
	}
	return sb.String(), starts, nil
}

// run is one converter under one quality setting.
type run struct {
	label string // "xberg/fast"
	conv  converter
}

// result is what one run produced for one file.
type result struct {
	label      string
	elapsed    time.Duration
	text       string
	pageStarts []int
	err        error
}

func main() {
	cfgPath := flag.String("config", "server.yaml", "path to server config YAML (for converter URLs)")
	doclingURL := flag.String("docling", "", `docling-serve base URL (overrides config; "off" disables it)`)
	xbergURL := flag.String("xberg", "", `xberg base URL (overrides config; "off" disables it)`)
	fast := flag.Bool("fast", true, "fast mode: no OCR, no layout models")
	both := flag.Bool("both", false, "run each converter twice, fast and full-quality")
	dumpDir := flag.String("dump", "", "write each conversion's text to this directory")
	pagePreview := flag.Int("pages", 0, "print the first N runes of each mapped page")
	timeout := flag.Duration("timeout", 10*time.Minute, "per-conversion timeout")
	builtin := flag.Bool("builtin", true, "also run the built-in pure-Go PDFium extractor as a baseline")
	flag.Parse()

	files := flag.Args()
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "usage: extracttest [flags] <file.pdf> [file2 …]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	// Read the server config only when a flag actually needs it. A missing
	// config is not fatal — the flags alone are enough to run.
	var cfgDocling, cfgXberg string
	if *doclingURL == "" || *xbergURL == "" {
		if cfg, err := config.Load(*cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "note: could not read %s (%v); using flags only\n", *cfgPath, err)
		} else {
			cfgDocling, cfgXberg = cfg.Documents.Docling.BaseURL, cfg.Documents.Xberg.BaseURL
		}
	}
	dURL := resolveURL(*doclingURL, cfgDocling)
	xURL := resolveURL(*xbergURL, cfgXberg)

	modes := []bool{*fast}
	if *both {
		modes = []bool{true, false}
	}

	var runs []run
	// The built-in extractor is the baseline every converter has to beat: it is
	// what the server uses when none is configured. One entry, not one per mode
	// — it has no quality setting.
	if *builtin {
		ex, err := pdfextract.New()
		if err != nil {
			fmt.Fprintf(os.Stderr, "builtin extractor unavailable: %v\n", err)
		} else {
			defer ex.Close()
			runs = append(runs, run{label: "builtin", conv: builtinConverter{ex: ex}})
		}
	}
	for _, f := range modes {
		suffix := "/full"
		if f {
			suffix = "/fast"
		}
		if dURL != "" {
			runs = append(runs, run{label: "docling" + suffix, conv: docling.New(dURL, *timeout, f)})
		}
		if xURL != "" {
			runs = append(runs, run{label: "xberg" + suffix, conv: xberg.New(xURL, *timeout, f)})
		}
	}
	if len(runs) == 0 {
		fmt.Fprintln(os.Stderr, "no converters configured: pass -docling and/or -xberg, or set them in server.yaml")
		os.Exit(2)
	}

	ctx := context.Background()
	fmt.Println("converters:")
	for _, r := range runs {
		health := "up"
		if !r.conv.Healthy(ctx) {
			health = "DOWN — conversions will fail"
		}
		fmt.Printf("  %-16s %s\n", r.label, health)
	}

	if *dumpDir != "" {
		if err := os.MkdirAll(*dumpDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "dump dir: %v\n", err)
			os.Exit(1)
		}
	}

	failures := 0
	for _, path := range files {
		if !compareFile(ctx, path, runs, *dumpDir, *pagePreview) {
			failures++
		}
	}
	if failures > 0 {
		fmt.Printf("\n%d of %d file(s) had at least one failed conversion\n", failures, len(files))
		os.Exit(1)
	}
}

// resolveURL picks a converter's base URL. An unset flag defers to the config;
// "off" disables the converter outright, which an empty flag cannot express —
// without it there is no way to exclude a converter that server.yaml configures
// but that is not currently running.
func resolveURL(flagVal, cfgVal string) string {
	switch flagVal {
	case "":
		return cfgVal
	case "off":
		return ""
	default:
		return flagVal
	}
}

// compareFile runs every converter over one file and prints the comparison.
// It returns false when any conversion failed.
func compareFile(ctx context.Context, path string, runs []run, dumpDir string, pagePreview int) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s: %v\n", path, err)
		return false
	}
	name := filepath.Base(path)
	// PDFs are the paged path (the one that must preserve page citations);
	// everything else goes through the unpaged one, as the server does.
	paged := strings.EqualFold(filepath.Ext(name), ".pdf")

	fmt.Printf("\n═══ %s (%s, %s)\n", name, humanBytes(len(data)), pagedLabel(paged))

	results := make([]result, 0, len(runs))
	for _, r := range runs {
		res := result{label: r.label}
		start := time.Now()
		if paged {
			res.text, res.pageStarts, res.err = r.conv.ConvertPaged(ctx, name, data)
		} else {
			res.text, res.err = r.conv.Convert(ctx, name, data)
		}
		res.elapsed = time.Since(start)
		results = append(results, res)

		if res.err == nil && dumpDir != "" {
			out := filepath.Join(dumpDir, fmt.Sprintf("%s.%s.md", name, strings.ReplaceAll(res.label, "/", "-")))
			if err := os.WriteFile(out, []byte(res.text), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "  dump %s: %v\n", out, err)
			}
		}
	}

	printTable(results)
	printPageChecks(results, pagePreview)
	printAgreement(results)

	for _, res := range results {
		if res.err != nil {
			return false
		}
	}
	return true
}

// printTable is the headline comparison: speed, volume, and how much structure
// each converter recovered. Heading and table counts are the cheap proxy for
// "did layout analysis actually do anything".
func printTable(results []result) {
	fmt.Printf("\n  %-16s %9s %9s %7s %7s %7s %7s %7s %7s %7s\n",
		"converter", "time", "chars", "pages", "head", "table", "bullet", "num", "alpha", "empty%")
	for _, res := range results {
		if res.err != nil {
			fmt.Printf("  %-16s %9s  FAILED: %v\n", res.label, round(res.elapsed), res.err)
			continue
		}
		s := structureOf(res.text)
		fmt.Printf("  %-16s %9s %9d %7d %7d %7d %7d %7d %7d %6.1f%%\n",
			res.label, round(res.elapsed), utf8.RuneCountInString(res.text), len(res.pageStarts),
			s.headings, s.tableRows, s.bullets, s.numbered, s.lettered, s.whitespacePct)
	}
}

// printPageChecks validates each page mapping against the text it describes.
// A mapping that is not monotonic, or that runs past the end of the text, would
// mis-cite pages in every chunk of the document — a silent corruption, so it is
// worth failing loudly here rather than discovering it in a citation.
func printPageChecks(results []result, preview int) {
	any := false
	for _, res := range results {
		if res.err != nil || len(res.pageStarts) == 0 {
			continue
		}
		any = true
		runes := []rune(res.text)
		var problems []string
		for i, off := range res.pageStarts {
			if off < 0 || off > len(runes) {
				problems = append(problems, fmt.Sprintf("page %d offset %d outside text (%d runes)", i+1, off, len(runes)))
			}
			if i > 0 && off < res.pageStarts[i-1] {
				problems = append(problems, fmt.Sprintf("page %d starts before page %d", i+1, i))
			}
		}
		if res.pageStarts[0] != 0 {
			problems = append(problems, fmt.Sprintf("page 1 starts at %d, not 0 (text before the first page marker)", res.pageStarts[0]))
		}
		status := "ok"
		if len(problems) > 0 {
			status = "PROBLEMS"
		}
		fmt.Printf("\n  page mapping %s: %d pages, %s\n", res.label, len(res.pageStarts), status)
		for _, p := range problems {
			fmt.Printf("    ! %s\n", p)
		}
		if preview > 0 {
			for i, off := range res.pageStarts {
				if off > len(runes) {
					continue
				}
				end := min(off+preview, len(runes))
				fmt.Printf("    p%-3d %s\n", i+1, oneLine(string(runes[off:end])))
			}
		}
	}
	if !any {
		fmt.Println("\n  page mapping: none produced (unpaged source, or the converter returned no page data)")
	}
}

// printAgreement cross-checks the converters against each other. Identical text
// is not expected — they render Markdown differently — so this reports the
// overlap of their word multisets, which is insensitive to formatting but
// catches one converter dropping whole sections (or a scanned page needing OCR
// that only one of them ran).
func printAgreement(results []result) {
	ok := make([]result, 0, len(results))
	for _, r := range results {
		if r.err == nil {
			ok = append(ok, r)
		}
	}
	if len(ok) < 2 {
		return
	}
	fmt.Println("\n  word overlap (how much of each converter's text the other also found):")
	for i := 0; i < len(ok); i++ {
		for j := i + 1; j < len(ok); j++ {
			a, b := ok[i], ok[j]
			shared, aOnly, bOnly := wordOverlap(a.text, b.text)
			total := shared + aOnly
			totalB := shared + bOnly
			fmt.Printf("    %s ↔ %s: %.1f%% of %s, %.1f%% of %s (%d shared words)\n",
				a.label, b.label, pct(shared, total), a.label, pct(shared, totalB), b.label, shared)
			if aOnly > 0 && pct(shared, total) < 90 {
				fmt.Printf("      %s has %d words the other missed\n", a.label, aOnly)
			}
			if bOnly > 0 && pct(shared, totalB) < 90 {
				fmt.Printf("      %s has %d words the other missed\n", b.label, bOnly)
			}
		}
	}
}

// structure counts the Markdown features that only survive when a converter
// actually understood the document's layout.
//
// The three list counts are kept apart on purpose. In a regulation or contract
// the enumerator *is* part of the citation — "Article 8.7(b)" is not "Article
// 8.7 item 3" — so a converter that renumbers lettered sub-items into a numeric
// sequence, or drops the numbers in favour of plain bullets, has changed what
// the document says while leaving the prose intact and the word counts healthy.
// Comparing numbered against lettered counts across converters exposes that;
// a single "list items" total hides it.
type structure struct {
	headings      int
	tableRows     int
	bullets       int // "- x" / "* x": an item whose original marker is gone
	numbered      int // "1. x"
	lettered      int // "(a) x" / "a) x"
	whitespacePct float64
}

// letteredMarker matches a lettered enumerator at the start of a line, the form
// legal drafting uses for sub-items.
var letteredMarker = regexp.MustCompile(`^\(?[a-zA-Z]{1,2}\)\s+\S`)

// numberedMarker matches an ordered-list marker at the start of a line.
var numberedMarker = regexp.MustCompile(`^\(?\d{1,3}[.)]\s+\S`)

func structureOf(text string) structure {
	var s structure
	blank := 0
	lines := strings.Split(text, "\n")
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case t == "":
			blank++
		case strings.HasPrefix(t, "#"):
			s.headings++
		case strings.Contains(t, "|"):
			s.tableRows++
		default:
			// A converter that turns "(a) …" into a Markdown bullet still keeps
			// the original enumerator as text ("- (a) …"). Strip the bullet
			// before classifying, so the count reflects which enumerator the
			// reader ends up seeing rather than how it was marked up.
			item := t
			if rest, ok := strings.CutPrefix(item, "- "); ok {
				item = strings.TrimSpace(rest)
			} else if rest, ok := strings.CutPrefix(item, "* "); ok {
				item = strings.TrimSpace(rest)
			}
			switch {
			case numberedMarker.MatchString(item):
				s.numbered++
			case letteredMarker.MatchString(item):
				s.lettered++
			case item != t:
				s.bullets++ // a bullet whose original enumerator is gone
			}
		}
	}
	if len(lines) > 0 {
		s.whitespacePct = float64(blank) * 100 / float64(len(lines))
	}
	return s
}

// wordOverlap compares two texts as multisets of lowercased word tokens.
func wordOverlap(a, b string) (shared, aOnly, bOnly int) {
	ca, cb := wordCounts(a), wordCounts(b)
	for w, na := range ca {
		nb := cb[w]
		shared += min(na, nb)
		if na > nb {
			aOnly += na - nb
		}
	}
	for w, nb := range cb {
		if na := ca[w]; nb > na {
			bOnly += nb - na
		}
	}
	return shared, aOnly, bOnly
}

func wordCounts(s string) map[string]int {
	out := map[string]int{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r == '\'' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		out[f]++
	}
	return out
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100 / float64(total)
}

func pagedLabel(paged bool) string {
	if paged {
		return "paged"
	}
	return "unpaged"
}

// oneLine collapses whitespace so a page preview stays on one terminal line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
