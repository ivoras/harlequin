package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ivoras/harlequin/internal/server/websearch"
)

// webSearchMaxResults caps how many hits one WebSearch call returns to the
// model (the API is queried deeper so domain filtering has room to drop hits).
const webSearchMaxResults = 10

// webSearchEntry registers the WebSearch tool (Brave Search API). The tool's
// signature mirrors Claude's WebSearch: query + optional allowed_domains /
// blocked_domains.
func (a *Agent) webSearchEntry() toolEntry {
	return toolEntry{
		def: fnTool("WebSearch", `
- Allows the assistant to search the web and use the results to inform responses
- Provides up-to-date data for current events and recent information
- Returns search result information: title, URL, snippet, and age
- Use this for information past the model's knowledge cutoff, or to find pages to read with WebFetch
- Results are ranked most-relevant first
Usage notes:
  - Domain filtering is supported to include or block specific domains
  - Cite the result URLs when their content informs your answer
`, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "minLength": 2,
					"description": "The search query to use"},
				"allowed_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "Only include search results from these domains"},
				"blocked_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
					"description": "Never include search results from these domains"},
			},
			"required": []string{"query"},
		}),
		handler: func(ctx context.Context, rc *runContext, args map[string]any) (string, error) {
			query := strings.TrimSpace(argString(args, "query"))
			if len(query) < 2 {
				return "error: query is required (at least 2 characters)", nil
			}
			allowed := coerceStringSlice(args["allowed_domains"])
			blocked := coerceStringSlice(args["blocked_domains"])
			results, err := a.WebSearch.Search(ctx, query, 20)
			if err != nil {
				return "error: " + err.Error(), nil
			}
			results = websearch.FilterDomains(results, allowed, blocked)
			if len(results) > webSearchMaxResults {
				results = results[:webSearchMaxResults]
			}
			if len(results) == 0 {
				return fmt.Sprintf("No results for %q (after domain filtering). Try different terms or fewer domain restrictions.", query), nil
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Web search results for query: %q\n\n", query)
			for i, r := range results {
				fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
				if r.Description != "" {
					desc := r.Description
					if r.Age != "" {
						desc += " (" + r.Age + ")"
					}
					fmt.Fprintf(&sb, "   %s\n", desc)
				}
			}
			sb.WriteString("\nUse WebFetch on a result URL to read the full page.")
			return sb.String(), nil
		},
	}
}
