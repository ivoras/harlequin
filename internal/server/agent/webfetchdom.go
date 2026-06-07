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
- Fetches a web page and returns its HTML structure as JSON so you can locate data precisely.
- Use grep="<text on the page>" to find the deepest element(s) containing that text — each result includes a CSS "path" you can reuse.
- Use selector="<css>" to return the elements matching a CSS selector.
- With neither, returns a shallow structural skeleton; narrow down with grep=/selector=.
- The full page HTML is saved to a tmp:// handle so you can re-query it with run_js (dom.parse(tmp.read(handle))) without re-fetching.
- Workflow: use this to discover the path to the data once, then write a small run_js parser that reads that path on every future check — no AI needed after setup.
`

// webFetchDOMResultCap bounds the JSON returned to the (small) model.
const webFetchDOMResultCap = 6000

func webFetchDOMToolDef() llm.Tool {
	return fnTool("WebFetchDOM", webFetchDOMDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":         map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch"},
			"grep":        map[string]any{"type": "string", "description": "Find the deepest element(s) whose text or attribute contains this string"},
			"selector":    map[string]any{"type": "string", "description": "CSS selector of element(s) to return"},
			"max_matches": map[string]any{"type": "integer", "description": "Cap on returned nodes (default 20)"},
			"max_depth":   map[string]any{"type": "integer", "description": "Skeleton depth when no grep/selector (default 3)"},
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
	maxMatches := argInt(args, "max_matches", 20)
	maxDepth := argInt(args, "max_depth", 3)

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
	// it) without another network round-trip.
	handle := "page-" + shortHash(raw.FinalURL) + ".html"
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
	if handle != "" {
		fmt.Fprintf(&sb, "Saved full HTML to tmp://%s — re-query with run_js: var h=dom.parse(tmp.read(%q)); dom.query(h, \"<css>\")\n", handle, handle)
	}

	switch {
	case grep != "":
		hits, err := d.Grep(grep, dom.GrepOptions{IgnoreCase: true, Attrs: true, MaxMatches: maxMatches})
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		fmt.Fprintf(&sb, "grep %q matched %d node(s):\n%s", grep, len(hits), marshalCapped(hits))
	case selector != "":
		nodes, err := d.Query(selector, 0)
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		shown := nodes
		if maxMatches > 0 && len(shown) > maxMatches {
			shown = shown[:maxMatches]
		}
		fmt.Fprintf(&sb, "selector %q matched %d node(s)%s:\n%s", selector, len(nodes),
			truncNote(len(nodes), len(shown)), marshalCapped(shown))
	default:
		sk, err := d.Skeleton(dom.SkelOptions{MaxDepth: maxDepth, MaxChildren: 40, Paths: true})
		if err != nil {
			return fmt.Sprintf("error: %v", err), nil
		}
		fmt.Fprintf(&sb, "Structure (depth %d) — narrow with grep=\"<text>\" or selector=\"<css>\":\n%s", maxDepth, marshalCapped(sk))
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
