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
)

// webFetchDOMDescription is advertised to the model.
const webFetchDOMDescription = `
- Fetches a web page and returns a JSON summary of its structure AS THE TOOL RESULT (read it directly; it is the answer, not something to feed back into run_js).
- With NO grep/selector, returns "candidate lists" — repeating elements with a ready-to-use CSS selector, a count, and a text sample of the first member; plus a page skeleton.
- TO LIST / COMPARE / FILTER / COUNT ITEMS IN A LIST (e.g. "the products on this page", "which items mention X", "how many results"): call with selector="<the candidate list's selector>". When the selector matches MANY elements it returns one record per item — each with its text and links — so you read and reason over the whole list in one call. Pick the list whose sample is the items you want; do NOT grep fields one by one.
- To locate a single specific value, call again with grep="<text that appears on the page>" — each match comes with its surrounding elements (the nearby name/label/link) and the match's CSS path.
- To inspect one element, call with a selector that matches a single node — it returns that element's detail.
- Finding, confirming, and extracting a list needs ONLY this tool — you do NOT need run_js.
- Separately, the page's RAW HTML (not JSON) is saved to a tmp:// handle. It is a DATA file, not a script. You only need it if you later write a run_js parser, and then ONLY via dom.parse(tmp.read(handle)) inside run_js 'code' — never pass the handle as run_js 'script' (it expects JS and fails).
- Prefer this tool with the grep or selector instead of WebFetch if the page is expected to be large and complex.
`

// recordListThreshold is the match count at or above which a selector is treated
// as a list and rendered as comparable records (text + links + numbers) rather
// than as element detail.
const recordListThreshold = 2

// webFetchDOMResultCap bounds the JSON returned to the (small) model.
const webFetchDOMResultCap = 15000

func webFetchDOMToolDef() llm.Tool {
	return fnTool("WebFetchDOM", webFetchDOMDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":         map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch"},
			"grep":        map[string]any{"type": "string", "description": "Find the deepest element(s) whose text or attribute contains this string"},
			"selector":    map[string]any{"type": "string", "description": "CSS selector of element(s) to return"},
			"max_matches": map[string]any{"type": "integer", "description": "Cap on returned nodes (default 20)"},
			"max_depth":   map[string]any{"type": "integer", "description": "Skeleton depth when no grep/selector (default 3)"},
			"context":     map[string]any{"type": "integer", "description": "Neighbouring elements to include around each grep/single match — sibling and ancestor levels — so a value is read together with its nearby label/name/link (default 3; 0 disables)"},
			"save_file":   map[string]any{"type": "string", "description": "If set, save the fetched page under this name in the tmp:// namespace (e.g. \"links.html\"). The result returns the full path (e.g. tmp://links.html) for use with the Grep tool."},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	})
}

func (a *Agent) webFetchDOMEntry() toolEntry {
	return toolEntry{
		def:     webFetchDOMToolDef(),
		handler: a.webFetchDOM,
	}
}

func (a *Agent) webFetchDOM(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
	if a.WebFetcher == nil {
		return "error: web fetching is not enabled on this server", nil
	}
	rawURL := strings.TrimSpace(argString(args, "url"))
	if rawURL == "" {
		return "error: url is required", nil
	}
	selector := strings.TrimSpace(argString(args, "selector"))
	grep := strings.TrimSpace(argString(args, "grep"))
	saveFile := strings.TrimSpace(argString(args, "save_file"))
	maxMatches := argInt(args, "max_matches", 20)
	maxDepth := argInt(args, "max_depth", 3)
	context := argInt(args, "context", 3)

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
	handle := "page-" + shortHash(raw.FinalURL) + ".html"
	if saveFile != "" {
		handle = saveFile
	}
	if err := a.tmpRoot(rc.userID).Write(handle, raw.Body); err != nil {
		log.Printf("webfetchdom: could not save handle %s: %v", handle, err)
		handle = ""
	}

	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"url": rawURL, "final_url": raw.FinalURL, "dom": true, "ok": true,
		"cached": raw.Cached, "fetch_ms": fetchMS, "bytes": len(raw.Body),
		"grep": grep, "selector": selector, "handle": handle,
	})
	log.Printf("webfetchdom: GET %s (cached=%v, %dms, %d bytes, grep=%q, selector=%q)",
		raw.FinalURL, raw.Cached, fetchMS, len(raw.Body), grep, selector)

	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", raw.FinalURL)
	if title := firstText(d, "title"); title != "" {
		fmt.Fprintf(&sb, "Title: %s\n", title)
	}
	if saveFile != "" && handle != "" {
		fmt.Fprintf(&sb, "Saved page to tmp://%s — search it with the Grep tool, e.g. Grep(pattern=\"…\", path=\"tmp://%s\").\n", handle, handle)
	}
	if handle != "" {
		fmt.Fprintf(&sb, "(The page's raw HTML is also saved to tmp://%s — data, NOT json and NOT a script. You don't need it to pick a selector: call WebFetchDOM again with grep= or selector=. Only if writing a run_js parser, read it in `code`: var h=dom.parse(tmp.read(%q)); println(dom.query(h, \"<css>\").length))\n", handle, handle)
	}

	cx := dom.ContextOptions{Siblings: context, Ancestors: context, TextChars: 110}
	switch {
	case grep != "":
		gopts := dom.GrepOptions{IgnoreCase: true, Attrs: true, MaxMatches: maxMatches}
		if context > 0 {
			hits, err := d.GrepContext(grep, gopts, cx)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
			}
			// The match keeps its Path (so the model can act on/re-query the exact
			// element); the surrounding context nodes are pathless (read for content).
			fmt.Fprintf(&sb, "grep %q matched %d node(s) (each with its surrounding elements — read the value together with the nearby name/label/link):\n%s", grep, len(hits), marshalCapped(hits))
		} else {
			hits, err := d.Grep(grep, gopts)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
			}
			fmt.Fprintf(&sb, "grep %q matched %d node(s):\n%s", grep, len(hits), marshalCapped(hits))
		}
	case selector != "":
		nodes, err := d.Query(selector, 0)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		// A selector matching many elements is a list: return one comparable
		// record per item (text + links + parsed numbers) so the model can
		// rank/filter (cheapest, highest, under N) in this single call. A
		// single match stays element detail.
		if len(nodes) >= recordListThreshold {
			recs, err := d.Records(selector, maxMatches, 110)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
			}
			// Drop the deep nth-of-type Path from bulk rows — it dominates bytes and
			// isn't needed to read or compare items; re-query a single item for its
			// path if you need to act on one. Keep one link per item (usually the
			// item's own page); the rest are image/icon noise.
			for i := range recs {
				recs[i].Path = ""
				if len(recs[i].Links) > 1 {
					recs[i].Links = recs[i].Links[:1]
				}
			}
			fmt.Fprintf(&sb, "selector %q matched %d item(s)%s — records (one per item: text + links; read, compare, filter, or count these):\n%s",
				selector, len(nodes), truncNote(len(nodes), len(recs)), marshalCapped(recs))
		} else if context > 0 {
			mcs, err := d.QueryContext(selector, maxMatches, cx)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
			}
			for i := range mcs {
				mcs[i].Match.Path = ""
			}
			fmt.Fprintf(&sb, "selector %q matched %d node(s) (with surrounding elements):\n%s", selector, len(nodes), marshalCapped(mcs))
		} else {
			stripPaths(nodes)
			fmt.Fprintf(&sb, "selector %q matched %d node(s):\n%s", selector, len(nodes), marshalCapped(nodes))
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
	return sb.String(), nil
}

// firstText returns the trimmed text of the first element matching selector.
func firstText(d *dom.Doc, selector string) string {
	nodes, err := d.Query(selector, 0)
	if err != nil || len(nodes) == 0 {
		return ""
	}
	return nodes[0].Text
}

// stripPaths drops the (large, deep nth-of-type) Path from node summaries; the
// model locates elements with grep=/selector=, so paths only cost tokens.
func stripPaths(ns []dom.Node) {
	for i := range ns {
		ns[i].Path = ""
	}
}

// marshalCapped renders v as JSON, bounded so a large page can't flood the model.
func marshalCapped(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("(could not render: %v)", err)
	}
	if len(b) > webFetchDOMResultCap {
		return string(b[:webFetchDOMResultCap]) + "\n…[truncated — use a tighter grep/selector or run_js against the saved handle]"
	}
	return string(b)
}

func truncNote(total, shown int) string {
	if total > shown {
		return fmt.Sprintf(" (showing first %d)", shown)
	}
	return ""
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
