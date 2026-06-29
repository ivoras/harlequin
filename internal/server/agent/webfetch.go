package agent

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/webfetch"
	"github.com/ivoras/harlequin/internal/shared/types"
)

// webFetchDescription is the tool description advertised to the model.
const webFetchDescription = `
- Fetches a web page from a given URL and analyzes it with AI using a prompt.
- Input: a valid URL (auto-upgrades HTTP→HTTPS) and a prompt describing desired info.
- Fetches, converts HTML to markdown, summarizes large results.
- Returns the AI model’s response about the page content.
- 15-minute cache for repeated URLs.
- Redirects: tool tells you if URL changed; call again with new URL.
- Pagination: if a listing spans multiple pages (next/"page 2" links), fetch each page and combine — don't answer from page 1 alone.
`

const (
	// webFetchDefaultPrompt is used when the caller passes an empty prompt.
	webFetchDefaultPrompt = "Extract raw facts from this scraped web page."
	// webFetchSystemPrompt is the simplified system prompt for the analysis call.
	webFetchSystemPrompt = "You extract information from scraped web pages. Pages usually link to related pages: whenever a link looks like it would add to your understanding of the question — more detail, supporting context, a cited source, a linked document, the next page of a list, or a section the current page only summarizes — follow it by calling the WebFetch tool again with that URL, then fold what you learn into your answer. Be deliberate, not exhaustive: follow only links that are genuinely relevant, prefer links on the same site, and stop once you can answer the question well. If neither the page nor the worthwhile links it points to contain the answer, say so plainly. For any arithmetic, use the calculator tool rather than computing it yourself."
	// webFetchMaxDepth bounds nested WebFetch calls made by the analysis model.
	webFetchMaxDepth = 2
	// webFetchMaxSteps bounds the analysis tool-calling loop per fetch.
	webFetchMaxSteps = 4
	// webFetchMaxContent caps how much markdown is sent to the analysis model.
	webFetchMaxContent = 60000
)

// webFetchToolDef is the shared tool definition (advertised to both the main
// agent and the analysis model).
func webFetchToolDef() llm.Tool {
	return fnTool("WebFetch", webFetchDescription, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url":    map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch content from"},
			"prompt": map[string]any{"type": "string", "description": "The prompt to run on the fetched content"},
		},
		"required":             []string{"url", "prompt"},
		"additionalProperties": false,
	})
}

// webFetchEntry registers the WebFetch tool when a fetcher is configured.
func (a *Agent) webFetchEntry() toolEntry {
	return toolEntry{
		def: webFetchToolDef(),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			return a.webFetch(ctx, rc, args, 0, map[string]bool{})
		},
	}
}

// webFetch fetches the URL, converts it to markdown, and runs the analysis model
// over it. depth bounds recursive WebFetch calls issued by the analysis model;
// seen records the URLs already fetched in this chain so the analysis model can't
// recurse back into a page it has already retrieved.
func (a *Agent) webFetch(ctx context.Context, rc *runContext, args map[string]any, depth int, seen map[string]bool) (string, error) {
	if a.WebFetcher == nil {
		return "error: web fetching is not enabled on this server", nil
	}
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "error: url is required", nil
	}
	key := normalizeURL(rawURL)
	if seen[key] {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"url": rawURL, "depth": depth, "ok": false, "skipped": "already_seen",
		})
		log.Printf("webfetch: skipping already-seen URL %s (depth=%d)", rawURL, depth)
		return fmt.Sprintf("error: %s was already fetched in this WebFetch chain; use the content already provided instead of fetching it again", rawURL), nil
	}
	seen[key] = true
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = webFetchDefaultPrompt
	}

	fetchStart := time.Now()
	res, err := a.WebFetcher.Fetch(ctx, rawURL)
	fetchDur := time.Since(fetchStart)
	fetchMS := fetchDur.Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"url": rawURL, "depth": depth, "ok": false,
			"error": err.Error(), "fetch_ms": fetchMS,
		})
		log.Printf("webfetch: GET %s failed after %s (depth=%d): %v", rawURL, fmtDur(fetchDur), depth, err)
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}
	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"url": rawURL, "final_url": res.FinalURL, "depth": depth, "ok": true,
		"cached": res.Cached, "fetch_ms": fetchMS, "via_zyte": res.ViaZyte,
		"bytes": len(res.Markdown), "title": res.Title,
	})
	target := rawURL
	if res.FinalURL != "" && res.FinalURL != rawURL {
		target = rawURL + " -> " + res.FinalURL // show redirect/upgrade only when it changed
	}
	log.Printf("webfetch: GET %s (cached=%v, %s, %d bytes, depth=%d)",
		target, res.Cached, fmtDur(fetchDur), len(res.Markdown), depth)

	// A redirect can land on a different URL; record it too so the analysis model
	// can't re-fetch the resolved page under its final address.
	if res.FinalURL != "" {
		seen[normalizeURL(res.FinalURL)] = true
	}

	content := res.Markdown
	if len(content) > webFetchMaxContent {
		content = content[:webFetchMaxContent] + "\n\n[content truncated]"
	}

	return a.analyzeWeb(ctx, rc, webFetchLabel, prompt, res, content, depth, seen)
}

// webDelegateLabel labels a delegated analysis call: source is the user-facing
// SSE Source (lets the client tell which tool spawned the sub-call), delegate is
// the stable machine tag recorded in trajectory logs.
type webDelegateLabel struct {
	source   string
	delegate string
}

var (
	webFetchLabel    = webDelegateLabel{source: "WebFetch", delegate: "web_fetch"}
	webFetchDOMLabel = webDelegateLabel{source: "WebFetchDOM", delegate: "web_fetch_dom"}
)

// analyzeWeb runs the small, fast analysis model over the fetched content. The
// model is given WebFetch, WebFetchDOM, and calculator; nested fetches are
// executed up to webFetchMaxDepth so it can follow a link if needed. label
// identifies the originating tool (WebFetch vs WebFetchDOM) for progress events
// and logs.
func (a *Agent) analyzeWeb(ctx context.Context, rc *runContext, label webDelegateLabel, prompt string, res webfetch.Result, content string, depth int, seen map[string]bool) (string, error) {
	var userMsg strings.Builder
	userMsg.WriteString(prompt)
	userMsg.WriteString("\n\nURL: ")
	userMsg.WriteString(res.FinalURL)
	if res.Title != "" {
		userMsg.WriteString("\nTitle: ")
		userMsg.WriteString(res.Title)
	}
	userMsg.WriteString("\n\n--- BEGIN PAGE CONTENT (Markdown) ---\n")
	userMsg.WriteString(content)
	userMsg.WriteString("\n--- END PAGE CONTENT ---")

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: webFetchSystemPrompt},
		{Role: llm.RoleUser, Content: userMsg.String()},
	}
	// Offer the analysis model WebFetch and WebFetchDOM (to follow links, in
	// markdown or DOM-structured form) and calculator (for any arithmetic over
	// figures on the page).
	calc := a.calculatorEntry()
	tools := []llm.Tool{webFetchToolDef(), webFetchDOMToolDef(), calc.def}

	var lastText string
	for step := 0; step < webFetchMaxSteps; step++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMRequest, map[string]any{
			"delegate": label.delegate, "model": a.WebFetchModel, "system": webFetchSystemPrompt,
			"prompt": prompt, "url": res.FinalURL, "content_chars": len(content),
			"depth": depth, "delegate_step": step + 1,
		})
		callStart := time.Now()
		// Surface this delegated call's prefill progress to the client, labeled so
		// it reads as the WebFetch sub-call rather than the user's own prompt.
		// Throttle to ~every 5% (reset per call), mirroring the main turn loop.
		lastPPPct := -1
		onProgress := func(pp *llm.PromptProgress) {
			if rc == nil || rc.emit == nil {
				return
			}
			// `processed` already includes the cached prefix, so discount cache on
			// both sides to measure real work 0→100% (see agent.go run loop).
			total := pp.Total - pp.Cache
			if total <= 0 {
				return
			}
			done := pp.Processed - pp.Cache
			if done < 0 {
				done = 0
			}
			pct := done * 100 / total
			if pct >= lastPPPct+5 || done >= total {
				lastPPPct = pct
				rc.emit(types.StreamEvent{
					Type:            types.SSEPromptProgress,
					Source:          label.source,
					PromptProcessed: done,
					PromptTotal:     total,
				})
			}
		}
		text, toolCalls, err := a.completeOnceProgress(ctx, llm.ChatRequest{
			Model:       a.WebFetchModel,
			Messages:    msgs,
			Tools:       tools,
			Temperature: llm.Ptr(a.WebFetchTemperature),
		}, onProgress)
		callDur := time.Since(callStart)
		callMS := callDur.Milliseconds()
		model := a.WebFetchModel
		if model == "" {
			model = "default"
		}
		if err != nil {
			a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMResponse, map[string]any{
				"delegate": label.delegate, "model": a.WebFetchModel, "depth": depth,
				"delegate_step": step + 1, "duration_ms": callMS, "error": err.Error(),
			})
			log.Printf("webfetch: delegated LLM (%s) step %d failed after %s: %v", model, step+1, fmtDur(callDur), err)
			return fmt.Sprintf("error: analysis model failed: %v", err), nil
		}
		a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMResponse, map[string]any{
			"delegate": label.delegate, "model": a.WebFetchModel, "depth": depth,
			"delegate_step": step + 1, "duration_ms": callMS,
			"content": text, "tool_calls": logToolCalls(toolCalls),
		})
		if len(toolCalls) > 0 {
			log.Printf("webfetch: delegated LLM (%s) step %d took %s (depth=%d, %d tool call(s): %s)",
				model, step+1, fmtDur(callDur), depth, len(toolCalls), formatToolCalls(toolCalls))
		} else {
			log.Printf("webfetch: delegated LLM (%s) step %d took %s (depth=%d, 0 tool calls)",
				model, step+1, fmtDur(callDur), depth)
		}
		lastText = text
		if len(toolCalls) == 0 {
			return text, nil
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			var out string
			switch tc.Function.Name {
			case "WebFetch":
				// Bound nested fetching; calculator and other non-fetch tools are
				// unaffected by the depth limit.
				if depth >= webFetchMaxDepth {
					out = "error: nested WebFetch depth limit reached; do not fetch more pages, answer from the content already provided"
				} else {
					out, _ = a.webFetch(ctx, rc, parseToolArgs(tc.Function.Arguments), depth+1, seen)
				}
			case "WebFetchDOM":
				if depth >= webFetchMaxDepth {
					out = "error: nested fetch depth limit reached; do not fetch more pages, answer from the content already provided"
				} else {
					out, _ = a.webFetchDOM(ctx, rc, parseToolArgs(tc.Function.Arguments), depth+1, seen)
				}
			case "calculator":
				out, _ = calc.handler(ctx, rc, parseToolArgs(tc.Function.Arguments))
			default:
				out = fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: out, ToolCallID: tc.ID, Name: tc.Function.Name})
		}
	}
	return lastText, nil
}

// normalizeURL canonicalizes a URL for "already seen" comparison: lowercased
// scheme/host, no fragment, and no trailing slash on the path. Falls back to the
// lowercased raw string if it doesn't parse.
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}

// formatToolCalls renders tool calls as "name(args), name(args)" for one-line
// logging, truncating each call's arguments to keep the line readable.
func formatToolCalls(calls []llm.ToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, tc := range calls {
		parts = append(parts, tc.Function.Name+"("+truncateArgs(tc.Function.Arguments, 200)+")")
	}
	return strings.Join(parts, ", ")
}

func truncateArgs(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace/newlines
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// fmtDur renders a duration for logs: minutes+seconds once it reaches a minute
// (e.g. "4m13s"), seconds with millis above a second ("1.235s"), else plain ms.
func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return d.Round(time.Second).String()
	case d >= time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
}

// completeOnce runs a single non-streaming-from-the-caller's-view completion,
// draining the provider stream and returning the assistant text and tool calls.
func (a *Agent) completeOnce(ctx context.Context, req llm.ChatRequest) (string, []llm.ToolCall, error) {
	return a.completeOnceProgress(ctx, req, nil)
}

// completeOnceProgress is completeOnce with an optional callback for the
// provider's live prompt-processing chunks (llama.cpp return_progress), letting
// a delegated call (e.g. WebFetch analysis) surface its prefill progress.
func (a *Agent) completeOnceProgress(ctx context.Context, req llm.ChatRequest, onProgress func(*llm.PromptProgress)) (string, []llm.ToolCall, error) {
	stream, err := a.Provider.Chat(ctx, req)
	if err != nil {
		return "", nil, err
	}
	var text string
	var toolCalls []llm.ToolCall
	for chunk := range stream {
		if chunk.Err != nil {
			return "", nil, chunk.Err
		}
		if chunk.PromptProgress != nil {
			if onProgress != nil {
				onProgress(chunk.PromptProgress)
			}
			continue
		}
		text += chunk.TextDelta
		if chunk.Done {
			toolCalls = chunk.ToolCalls
		}
	}
	return text, toolCalls, nil
}
