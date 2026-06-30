package agent

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

// webFetchGrepDescription is advertised to the model.
const webFetchGrepDescription = `
- Fetches a web page and greps its raw HTML for a regular expression, returning the matching lines — deterministic, with NO AI, so it cannot hallucinate or misread.
- Use this whenever you expect the answer to appear LITERALLY on the page and can describe it as a pattern: a price, currency amount, date, code, ID, SKU/model number, phone, email, rate, count, etc. It returns exactly what is in the HTML.
- pattern (required) is a case-insensitive RE2 regular expression applied to the raw HTML. For each match it returns the whole line if that line is 120 characters or shorter, otherwise ±60 characters of context around the match, trimmed to word boundaries (the matched text itself is always shown in full).
- prompt (optional): instead of returning the raw matches to you, hand them to a fast analysis model with your prompt and return that model's answer (like WebFetch), but over just the grepped lines — handy when the value needs light interpretation (e.g. picking the sale price among several matches).
- Prefer this over WebFetch when a regex can pinpoint the value; use WebFetch when the answer must be read or summarized from prose, or WebFetchDOM to explore structure/lists.
`

const (
	// webFetchGrepLineMax: lines this long or shorter are returned whole.
	webFetchGrepLineMax = 120
	// webFetchGrepContext: chars of context on each side of a match in a longer line.
	webFetchGrepContext = 60
	// webFetchGrepMaxMatches bounds how many lines/snippets are emitted.
	webFetchGrepMaxMatches = 200
	// webFetchGrepResultCap bounds the total result handed to the model.
	webFetchGrepResultCap = 15000
)

func webFetchGrepToolDef() llm.Tool {
	return fnTool("WebFetchGrep", webFetchGrepDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":     map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch"},
			"pattern": map[string]any{"type": "string", "description": "Case-insensitive RE2 regular expression to grep against the page's raw HTML"},
			"prompt":  map[string]any{"type": "string", "description": "If set, analyze the matched lines with a fast AI model using this prompt and return its answer (like WebFetch), instead of returning the raw matches"},
		},
		"required":             []string{"url", "pattern"},
		"additionalProperties": false,
	})
}

func (a *Agent) webFetchGrepEntry() toolEntry {
	return toolEntry{
		def: webFetchGrepToolDef(),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.webFetchGrep(ctx, rc, args, 0, map[string]bool{})
		},
	}
}

// webFetchGrep greps the fetched HTML for pattern. With a prompt it hands the
// matches to the analysis model (see analyzeWeb) instead of returning them raw.
// depth/seen bound and de-duplicate nested fetches the analysis model may issue.
func (a *Agent) webFetchGrep(ctx context.Context, rc *runContext, args map[string]any, depth int, seen map[string]bool) (string, error) {
	if a.WebFetcher == nil {
		return "error: web fetching is not enabled on this server", nil
	}
	rawURL := strings.TrimSpace(argString(args, "url"))
	if rawURL == "" {
		return "error: url is required", nil
	}
	pattern := strings.TrimSpace(argString(args, "pattern"))
	if pattern == "" {
		return "error: pattern is required", nil
	}
	prompt := strings.TrimSpace(argString(args, "prompt"))
	// Loop guard for nested calls: don't re-fetch a URL already retrieved in this
	// chain (by WebFetch/WebFetchDOM/WebFetchGrep).
	key := normalizeURL(rawURL)
	if seen[key] {
		return fmt.Sprintf("error: %s was already fetched in this chain; use the content already provided instead of fetching it again", rawURL), nil
	}
	seen[key] = true
	// Case-insensitive by design: this tool is for locating a value known to be
	// literally on the page, where case rarely matters.
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return fmt.Sprintf("error: invalid pattern %q: %v", pattern, err), nil
	}

	start := time.Now()
	raw, err := a.WebFetcher.FetchRaw(ctx, rawURL)
	fetchMS := time.Since(start).Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"url": rawURL, "grep": true, "ok": false, "error": err.Error(), "fetch_ms": fetchMS,
		})
		log.Printf("webfetchgrep: GET %s failed after %dms: %v", rawURL, fetchMS, err)
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}

	// A redirect can land on a different URL; record it too so a nested call can't
	// re-fetch the resolved page under its final address.
	if raw.FinalURL != "" {
		seen[normalizeURL(raw.FinalURL)] = true
	}

	matches := grepHTML(string(raw.Body), re)
	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"url": rawURL, "final_url": raw.FinalURL, "grep": true, "ok": true,
		"cached": raw.Cached, "fetch_ms": fetchMS, "bytes": len(raw.Body),
		"pattern": pattern, "matches": len(matches), "prompt": prompt,
	})
	log.Printf("webfetchgrep: GET %s (cached=%v, %dms, %d bytes, pattern=%q, %d match(es))",
		raw.FinalURL, raw.Cached, fetchMS, len(raw.Body), pattern, len(matches))

	var body strings.Builder
	if len(matches) == 0 {
		fmt.Fprintf(&body, "No HTML matched /%s/i.\n", pattern)
	} else {
		fmt.Fprintf(&body, "%d match(es) for /%s/i in the page HTML (whole line if ≤%d chars, else ±%d chars of context):\n",
			len(matches), pattern, webFetchGrepLineMax, webFetchGrepContext)
		for _, m := range matches {
			body.WriteString(m)
			body.WriteByte('\n')
		}
	}
	matchText := body.String()
	if len(matchText) > webFetchGrepResultCap {
		matchText = matchText[:webFetchGrepResultCap] + "\n…[truncated — narrow the pattern]"
	}

	// With a prompt, hand the matched lines to the analysis model instead of
	// returning them raw (same path as WebFetch/WebFetchDOM).
	if prompt != "" {
		result := webfetch.Result{FinalURL: raw.FinalURL}
		return a.analyzeWeb(ctx, rc, webFetchGrepLabel, prompt, result, matchText, depth, seen)
	}
	return "URL: " + raw.FinalURL + "\n" + matchText, nil
}

// grepHTML returns, for each line of body that matches re: the whole line when
// it is webFetchGrepLineMax chars or shorter, else one context snippet per match
// (±webFetchGrepContext chars, word-boundary trimmed). Exact-duplicate output is
// deduplicated, and the total is bounded by webFetchGrepMaxMatches.
func grepHTML(body string, re *regexp.Regexp) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		locs := re.FindAllStringIndex(line, -1)
		if len(locs) == 0 {
			continue
		}
		if len(line) <= webFetchGrepLineMax {
			add(strings.TrimSpace(line))
		} else {
			for _, m := range locs {
				add(contextSnippet(line, m[0], m[1]))
				if len(out) >= webFetchGrepMaxMatches {
					return out
				}
			}
		}
		if len(out) >= webFetchGrepMaxMatches {
			return out
		}
	}
	return out
}

// contextSnippet returns up to webFetchGrepContext chars on each side of the
// match [s,e) in line, aligned to UTF-8 and trimmed so neither edge cuts a word
// in half (the match itself is never trimmed). Ellipses mark truncation.
func contextSnippet(line string, s, e int) string {
	lo := s - webFetchGrepContext
	if lo < 0 {
		lo = 0
	}
	hi := e + webFetchGrepContext
	if hi > len(line) {
		hi = len(line)
	}
	// Align window edges to rune boundaries.
	for lo > 0 && !utf8.RuneStart(line[lo]) {
		lo--
	}
	for hi < len(line) && !utf8.RuneStart(line[hi]) {
		hi++
	}
	// Drop a partial leading word (advance until the char before lo is a
	// non-word byte), but never past the match start.
	for lo > 0 && lo < s && isWordByte(line[lo-1]) && isWordByte(line[lo]) {
		lo++
	}
	// Drop a partial trailing word, but never before the match end.
	for hi < len(line) && hi > e && isWordByte(line[hi-1]) && isWordByte(line[hi]) {
		hi--
	}
	snip := strings.TrimSpace(line[lo:hi])
	if snip == "" {
		snip = strings.TrimSpace(line[s:e])
	}
	if lo > 0 {
		snip = "…" + snip
	}
	if hi < len(line) {
		snip = snip + "…"
	}
	return snip
}

// isWordByte reports whether b is part of a "word" for boundary trimming. UTF-8
// non-ASCII bytes (>=0x80) count as word bytes so multibyte letters aren't split.
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		b >= 0x80
}
