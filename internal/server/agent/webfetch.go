package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/sessionlog"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

// webFetchDescription is the tool description advertised to the model.
const webFetchDescription = `
- Fetches a web page from a given URL and analyzes it with AI using a prompt.
- Input: a valid URL (auto-upgrades HTTP→HTTPS) and a prompt describing desired info.
- Fetches, converts HTML to markdown, summarizes large results.
- Returns the AI model’s response about the page content.
- 15-minute cache for repeated URLs.
- Redirects: tool tells you if URL changed; call again with new URL.
- For GitHub, use the gh CLI via Bash if possible.
`

const (
	// webFetchDefaultPrompt is used when the caller passes an empty prompt.
	webFetchDefaultPrompt = "Extract raw facts from this scraped web page."
	// webFetchSystemPrompt is the simplified system prompt for the analysis call.
	webFetchSystemPrompt = "You are a helpful assistent specialised in analysing web site content. Do not fetch the same URL multiple times."
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
			return a.webFetch(ctx, rc, args, 0)
		},
	}
}

// webFetch fetches the URL, converts it to markdown, and runs the analysis model
// over it. depth bounds recursive WebFetch calls issued by the analysis model.
func (a *Agent) webFetch(ctx context.Context, rc *runContext, args map[string]any, depth int) (string, error) {
	if a.WebFetcher == nil {
		return "error: web fetching is not enabled on this server", nil
	}
	rawURL, _ := args["url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "error: url is required", nil
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = webFetchDefaultPrompt
	}

	fetchStart := time.Now()
	res, err := a.WebFetcher.Fetch(ctx, rawURL)
	fetchMS := time.Since(fetchStart).Milliseconds()
	if err != nil {
		a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
			"url": rawURL, "depth": depth, "ok": false,
			"error": err.Error(), "fetch_ms": fetchMS,
		})
		log.Printf("webfetch: GET %s failed after %dms (depth=%d): %v", rawURL, fetchMS, depth, err)
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}
	a.logEvent(ctx, rc, sessionlog.TypeWebFetch, map[string]any{
		"url": rawURL, "final_url": res.FinalURL, "depth": depth, "ok": true,
		"cached": res.Cached, "fetch_ms": fetchMS,
		"bytes": len(res.Markdown), "title": res.Title,
	})
	target := rawURL
	if res.FinalURL != "" && res.FinalURL != rawURL {
		target = rawURL + " -> " + res.FinalURL // show redirect/upgrade only when it changed
	}
	log.Printf("webfetch: GET %s (cached=%v, %dms, %d bytes, depth=%d)",
		target, res.Cached, fetchMS, len(res.Markdown), depth)

	content := res.Markdown
	if len(content) > webFetchMaxContent {
		content = content[:webFetchMaxContent] + "\n\n[content truncated]"
	}

	return a.analyzeWeb(ctx, rc, prompt, res, content, depth)
}

// analyzeWeb runs the small, fast analysis model over the fetched markdown. The
// model is given the WebFetch tool only; nested calls are executed up to
// webFetchMaxDepth so it can follow a link if needed.
func (a *Agent) analyzeWeb(ctx context.Context, rc *runContext, prompt string, res webfetch.Result, content string, depth int) (string, error) {
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
	tools := []llm.Tool{webFetchToolDef()}

	var lastText string
	for step := 0; step < webFetchMaxSteps; step++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMRequest, map[string]any{
			"delegate": "web_fetch", "model": a.WebFetchModel, "system": webFetchSystemPrompt,
			"prompt": prompt, "url": res.FinalURL, "content_chars": len(content),
			"depth": depth, "delegate_step": step + 1,
		})
		callStart := time.Now()
		text, toolCalls, err := a.completeOnce(ctx, llm.ChatRequest{
			Model:       a.WebFetchModel,
			Messages:    msgs,
			Tools:       tools,
			Temperature: llm.Ptr(a.Temperature),
		})
		callMS := time.Since(callStart).Milliseconds()
		model := a.WebFetchModel
		if model == "" {
			model = "default"
		}
		if err != nil {
			a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMResponse, map[string]any{
				"delegate": "web_fetch", "model": a.WebFetchModel, "depth": depth,
				"delegate_step": step + 1, "duration_ms": callMS, "error": err.Error(),
			})
			log.Printf("webfetch: delegated LLM (%s) step %d failed after %dms: %v", model, step+1, callMS, err)
			return fmt.Sprintf("error: analysis model failed: %v", err), nil
		}
		a.logEvent(ctx, rc, sessionlog.TypeDelegatedLLMResponse, map[string]any{
			"delegate": "web_fetch", "model": a.WebFetchModel, "depth": depth,
			"delegate_step": step + 1, "duration_ms": callMS,
			"content": text, "tool_calls": logToolCalls(toolCalls),
		})
		if len(toolCalls) > 0 {
			log.Printf("webfetch: delegated LLM (%s) step %d took %dms (depth=%d, %d tool call(s): %s)",
				model, step+1, callMS, depth, len(toolCalls), formatToolCalls(toolCalls))
		} else {
			log.Printf("webfetch: delegated LLM (%s) step %d took %dms (depth=%d, 0 tool calls)",
				model, step+1, callMS, depth)
		}
		lastText = text
		if len(toolCalls) == 0 {
			return text, nil
		}
		// Beyond the depth limit, stop fetching further and return what we have.
		if depth >= webFetchMaxDepth {
			if strings.TrimSpace(text) != "" {
				return text, nil
			}
			return "(nested WebFetch depth limit reached)", nil
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text, ToolCalls: toolCalls})
		for _, tc := range toolCalls {
			var out string
			if tc.Function.Name == "WebFetch" {
				out, _ = a.webFetch(ctx, rc, parseToolArgs(tc.Function.Arguments), depth+1)
			} else {
				out = fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: out, ToolCallID: tc.ID, Name: tc.Function.Name})
		}
	}
	return lastText, nil
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

// completeOnce runs a single non-streaming-from-the-caller's-view completion,
// draining the provider stream and returning the assistant text and tool calls.
func (a *Agent) completeOnce(ctx context.Context, req llm.ChatRequest) (string, []llm.ToolCall, error) {
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
		text += chunk.TextDelta
		if chunk.Done {
			toolCalls = chunk.ToolCalls
		}
	}
	return text, toolCalls, nil
}
