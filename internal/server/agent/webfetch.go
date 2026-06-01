package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/llm"
	"github.com/ivoras/harlequin/internal/server/webfetch"
)

// webFetchDescription is the tool description advertised to the model.
const webFetchDescription = `
- Fetches content from a specified URL and processes it using an AI model
- Takes a URL and a prompt as input
- Fetches the URL content, converts HTML to markdown
- Processes the content with the prompt using a small, fast model
- Returns the model's response about the content
- Use this tool when you need to retrieve and analyze web content

Usage notes:
  - IMPORTANT: If an MCP-provided web fetch tool is available, prefer using that tool instead of this one, as it may have fewer restrictions.
  - The URL must be a fully-formed valid URL
  - HTTP URLs will be automatically upgraded to HTTPS
  - The prompt should describe what information you want to extract from the page
  - This tool is read-only and does not modify any files
  - Results may be summarized if the content is very large
  - Includes a self-cleaning 15-minute cache for faster responses when repeatedly accessing the same URL
  - When a URL redirects to a different host, the tool will inform you and provide the redirect URL in a special format. You should then make a new WebFetch request with the redirect URL to fetch the content.
  - For GitHub URLs, prefer using the gh CLI via Bash instead (e.g., gh pr view, gh issue view, gh api).
`

const (
	// webFetchDefaultPrompt is used when the caller passes an empty prompt.
	webFetchDefaultPrompt = "Extract raw facts from this scraped web page:"
	// webFetchSystemPrompt is the simplified system prompt for the analysis call.
	webFetchSystemPrompt = "You are a helpful assistent specialised in analysing web site content."
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
	if strings.TrimSpace(rawURL) == "" {
		return "error: url is required", nil
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		prompt = webFetchDefaultPrompt
	}

	res, err := a.WebFetcher.Fetch(ctx, rawURL)
	if err != nil {
		return fmt.Sprintf("error: failed to fetch %s: %v", rawURL, err), nil
	}

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
		text, toolCalls, err := a.completeOnce(ctx, llm.ChatRequest{
			Model:       a.WebFetchModel,
			Messages:    msgs,
			Tools:       tools,
			Temperature: llm.Ptr(a.Temperature),
		})
		if err != nil {
			return fmt.Sprintf("error: analysis model failed: %v", err), nil
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

// webFetchResult was an alias; analysis uses webfetch.Result directly.

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
