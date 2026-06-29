package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/dom"
	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

// webFetchDOMDescription is advertised to the model.
const webFetchDOMDescription = `
- Fetches a web page, returns a structural view as the tool result, and saves the raw HTML under tmp:// (name it with save_file).
- With NO grep/selector: returns "candidate lists" (repeating elements, each with a ready-to-use CSS selector and a text sample) plus a page skeleton — use them to pick the selector for the items you want.
- selector="<css>" (comma-separated tag/class selectors, e.g. "div.product-card, li.item"): returns each matched element with its parent, up to 3 siblings, and up to 3 children as YAML, including text. Best for READING or LOCATING a handful of items, or confirming structure.
- grep="<regex>": flattens the page to one line per element (ancestor path + text) plus the page's links, and returns lines matching the case-insensitive regular expression with ±3 lines of context (use alternation, e.g. "price|€|\\$"). Best for locating one specific value.
- To EXTRACT MANY items and filter/sort/aggregate them (e.g. "the cheapest", "all that mention X", totals) — which needs computation — parse the saved page in run_js: var h=dom.parse(tmp.read("<handle>")); var items=dom.query(h, "<selector>"). Each node has .tag/.class/.attrs/.text, .getAttribute(name), .textContent, and is itself queryable (dom.query(node, sub)). This is the right tool for computed answers; do the comparison/sort there, not by eye.
- Pagination: a listing may span multiple pages (look for page links / "next" in the result). Fetch each page and combine — a single page is not the whole list.
- prompt="<question>" (REQUIRES grep or selector — error if used alone): runs the grep/selector result through a fast analysis model with your prompt and returns that model's answer, like WebFetch but over the targeted DOM slice. Point grep/selector at the data first, then prompt to extract it — e.g. grep="price|¥" + prompt="what's the price?". Without grep/selector it would only see the whole structural dump, which it can't reliably answer from; that's why it's rejected.
- Prefer this tool over WebFetch when you expect the result to be complex or large.
`

// webFetchDOMResultCap bounds the total result returned to the (small) model.
const webFetchDOMResultCap = 20000

func webFetchDOMToolDef() llm.Tool {
	return fnTool("WebFetchDOM", webFetchDOMDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":         map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch"},
			"prompt":      map[string]any{"type": "string", "description": "If set, analyze the grep/selector result with a fast AI model using this prompt and return its answer (like WebFetch). REQUIRES grep or selector to also be set so the model sees a focused slice; using prompt alone is an error"},
			"grep":        map[string]any{"type": "string", "description": "Case-insensitive regular expression to find in the flattened page (one line per element: ancestor path + text); returns matching lines with context"},
			"selector":    map[string]any{"type": "string", "description": "Comma-separated tag/class selectors (e.g. \"div.product-card, li.item\"); returns each match with parent/siblings/children as YAML"},
			"max_depth":   map[string]any{"type": "integer", "description": "Skeleton depth when no grep/selector (default 3)"},
			"context":     map[string]any{"type": "integer", "description": "Lines of context around each grep match in the flattened view (default 3)"},
			"save_file":   map[string]any{"type": "string", "description": "If set, save the fetched page under this name in the tmp:// namespace (e.g. \"links.html\"). The result returns the full path (e.g. tmp://links.html) for use with the Grep tool."},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	})
}

func (a *Agent) webFetchDOMEntry() toolEntry {
	return toolEntry{
		def: webFetchDOMToolDef(),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.webFetchDOM(ctx, rc, args, 0, map[string]bool{})
		},
	}
}

// webFetchDOM fetches the URL and returns a structural view (or, with a prompt,
// the analysis model's answer over that view — see analyzeWeb). depth/seen bound
// and de-duplicate nested fetches issued by the analysis model, mirroring
// webFetch so the two tools share one fetch chain.
func (a *Agent) webFetchDOM(ctx context.Context, rc *runContext, args map[string]any, depth int, seen map[string]bool) (string, error) {
	if a.WebFetcher == nil {
		return "error: web fetching is not enabled on this server", nil
	}
	rawURL := strings.TrimSpace(argString(args, "url"))
	if rawURL == "" {
		return "error: url is required", nil
	}
	selector := strings.TrimSpace(argString(args, "selector"))
	grep := strings.TrimSpace(argString(args, "grep"))
	prompt := strings.TrimSpace(argString(args, "prompt"))
	saveFile := strings.TrimSpace(argString(args, "save_file"))
	maxDepth := argInt(args, "max_depth", 3)
	context := argInt(args, "context", 3)

	// prompt analyzes a focused slice of the page, not the whole structural dump:
	// require a grep or selector to point at the data first. Validate before the
	// seen-guard/fetch so a corrected retry (same URL + grep) isn't rejected as a
	// duplicate. (The analysis model handles a targeted slice well but thrashes on
	// the full candidate-list/skeleton view.)
	if prompt != "" && grep == "" && selector == "" {
		return "error: prompt requires a query mode — also pass grep=\"<regex>\" (e.g. grep=\"price|¥\") or selector=\"<css>\" so the analysis model sees the relevant slice of the page, not the whole structural dump", nil
	}

	// Loop guard for nested calls: don't re-fetch a URL already retrieved in this
	// chain (by either WebFetch or WebFetchDOM).
	key := normalizeURL(rawURL)
	if seen[key] {
		return fmt.Sprintf("error: %s was already fetched in this chain; use the content already provided instead of fetching it again", rawURL), nil
	}
	seen[key] = true

	start := time.Now()
	raw, err := a.WebFetcher.FetchRaw(ctx, rawURL)
	fetchMS := time.Since(start).Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"url": rawURL, "dom": true, "ok": false, "error": err.Error(), "fetch_ms": fetchMS,
		})
		log.Printf("webfetchdom: GET %s failed after %dms: %v", rawURL, fetchMS, err)
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}
	d, err := dom.Parse(raw.Body)
	if err != nil {
		return fmt.Sprintf("error: failed to parse %s: %v", rawURL, err), nil
	}

	// Stash the raw HTML so the model can re-query it (or write a parser against
	// it) without another network round-trip. save_file lets the caller choose the
	// tmp:// name (e.g. to grep it with the Grep tool); otherwise a hashed name.
	saveName := "page-" + shortHash(raw.FinalURL) + ".html"
	if saveFile != "" {
		saveName = saveFile
	}
	handle := saveName
	var saveErr error
	if err := a.tmpRoot(rc.userID).Write(saveName, raw.Body); err != nil {
		log.Printf("webfetchdom: could not save handle %s: %v", saveName, err)
		saveErr = err
		handle = ""
	}

	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"url": rawURL, "final_url": raw.FinalURL, "dom": true, "ok": true,
		"cached": raw.Cached, "fetch_ms": fetchMS, "bytes": len(raw.Body),
		"grep": grep, "selector": selector, "handle": handle, "prompt": prompt,
	})
	log.Printf("webfetchdom: GET %s (cached=%v, %dms, %d bytes, grep=%q, selector=%q)",
		raw.FinalURL, raw.Cached, fetchMS, len(raw.Body), grep, selector)
	// A redirect can land on a different URL; record it too so a nested call can't
	// re-fetch the resolved page under its final address.
	if raw.FinalURL != "" {
		seen[normalizeURL(raw.FinalURL)] = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", raw.FinalURL)
	if title := firstText(d, "title"); title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", title)
	}
	if handle != "" {
		fmt.Fprintf(&sb, "(Full page saved to tmp://%s — to extract/compare many items, parse it in run_js: dom.parse(tmp.read(%q)); or search it with the Grep tool.)\n", handle, handle)
	} else if saveErr != nil {
		// The save failed (e.g. the tmp sandbox hit its storage quota). Tell the
		// model explicitly so it does NOT grep/parse a tmp://<name> that isn't
		// there — that silent gap previously caused a cascade of "no files" /
		// FSError retries. The structural result below is still usable.
		fmt.Fprintf(&sb, "WARNING: could not save the page to tmp://%s: %v — do NOT grep or parse that handle (it does not exist). Use the structural result below, or fall back to WebFetch for a one-shot answer.\n", saveName, saveErr)
	}

	switch {
	case grep != "":
		// Flatten the page to one line per element (allowed-tag ancestor path +
		// own text), plus the link list, grep all of it, and return ±context lines
		// per match.
		corpus := append([]string{"# Links on the website"}, d.LinkLines()...)
		res := d.GrepFlatten(grep, context, corpus)
		if res == "" {
			fmt.Fprintf(&sb, "grep %q: no matching lines in the flattened page.\n", grep)
		} else {
			fmt.Fprintf(&sb, "grep %q over the flattened page (one line per element: ancestorPath: text; \"> \" marks a match, ±%d lines of context):\n%s",
				grep, context, capStr(res))
		}
	case selector != "":
		// selector is a comma-separated list of tag/class selectors (CSS union).
		// For each match return its parent, nearest siblings, and children as YAML.
		fams, err := d.SelectFamily(selector, 0, 3, 3)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		if len(fams) == 0 {
			fmt.Fprintf(&sb, "selector %q matched no elements.\n", selector)
		} else {
			fmt.Fprintf(&sb, "selector %q matched %d element(s) — each with its parent, up to 3 siblings, and up to 3 children (YAML):\n%s",
				selector, len(fams), capStr(dom.FamiliesYAML(fams)))
		}
	default:
		// Lead with detected repeating lists — for "monitor a list" this usually
		// gives the model the selector directly (pick the one whose sample matches
		// the wanted data).
		if groups := d.RepeatingGroups(3, 15, 160); len(groups) > 0 {
			fmt.Fprintf(&sb, "Candidate lists (repeating elements; pick the selector whose sample is the data you want, then use it as the watch selector):\n%s\n\n", marshalCapped(groups))
		}
		sk, err := d.Skeleton(dom.SkelOptions{MaxDepth: maxDepth, MaxChildren: 40, Paths: false})
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		fmt.Fprintf(&sb, "Page structure (depth %d) — or narrow with grep=\"<text in an item>\" / selector=\"<css>\":\n%s", maxDepth, marshalCapped(sk))
	}

	// Always append the page's links so the model can navigate to detail/listing
	// pages it found.
	if links := d.LinkLines(); len(links) > 0 {
		sb.WriteString("\n\n# Links on the website\n")
		for _, l := range links {
			sb.WriteString("- " + l + "\n")
		}
	}
	structured := capStr(sb.String())

	// With a prompt, don't hand the structural view back to the main agent: run
	// the fast analysis model over it (same path as WebFetch) and return its
	// answer. The analysis model gets WebFetch/WebFetchDOM/calculator and can
	// follow links; `seen` (already holding this URL) bounds re-fetches.
	if prompt != "" {
		res := webfetch.Result{FinalURL: raw.FinalURL, Title: firstText(d, "title")}
		return a.analyzeWeb(ctx, rc, webFetchDOMLabel, prompt, res, structured, depth, seen)
	}
	return structured, nil
}

// firstText returns the trimmed text of the first element matching selector.
func firstText(d *dom.Doc, selector string) string {
	nodes, err := d.Query(selector, 0)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	return nodes[0].Text
}

// marshalCapped renders v as JSON, bounded so a large page can't flood the model.
func marshalCapped(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("(could not render: %v)", err)
	}
	return capStr(string(b))
}

// capStr bounds a rendered result to the model's result cap.
func capStr(s string) string {
	if len(s) > webFetchDOMResultCap {
		return s[:webFetchDOMResultCap] + "\n…[truncated — use a tighter grep/selector or run_js against the saved handle]"
	}
	return s
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// argString / argInt read tool args defensively (JSON numbers decode as float64).
func argString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}
