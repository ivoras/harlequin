// Package websearch is a minimal Brave Search API client backing the agent's
// WebSearch tool. An empty API key disables it (Configured reports false).
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIBase is the public Brave Search API host. Overridable (tests).
const DefaultAPIBase = "https://api.search.brave.com"

// Client queries the Brave Search API.
type Client struct {
	apiKey  string
	apiBase string
	http    *http.Client
}

// New builds a client. apiKey empty = disabled; apiBase defaults to
// DefaultAPIBase when empty.
func New(apiKey, apiBase string) *Client {
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	return &Client{
		apiKey:  strings.TrimSpace(apiKey),
		apiBase: strings.TrimRight(apiBase, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// Configured reports whether an API key is set.
func (c *Client) Configured() bool { return c != nil && c.apiKey != "" }

// Result is one web search hit.
type Result struct {
	Title       string
	URL         string
	Description string
	Age         string // human-readable freshness ("2 days ago"), often empty
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
		} `json:"results"`
	} `json:"web"`
}

// Search runs a web search and returns up to count results (Brave caps a
// request at 20).
func (c *Client) Search(ctx context.Context, query string, count int) ([]Result, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("websearch: no API key configured")
	}
	if count <= 0 || count > 20 {
		count = 20
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("count", strconv.Itoa(count))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.apiBase+"/res/v1/web/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("websearch: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var br braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return nil, fmt.Errorf("websearch: decode: %w", err)
	}
	out := make([]Result, 0, len(br.Web.Results))
	for _, r := range br.Web.Results {
		out = append(out, Result{Title: r.Title, URL: r.URL, Description: r.Description, Age: r.Age})
	}
	return out, nil
}

// FilterDomains applies the WebSearch tool's domain rules: when allowed is
// non-empty only results whose host is one of (or a subdomain of) those
// domains survive; results matching blocked are always dropped.
func FilterDomains(results []Result, allowed, blocked []string) []Result {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}
	out := make([]Result, 0, len(results))
	for _, r := range results {
		host := hostOf(r.URL)
		if host == "" {
			continue
		}
		if matchesAny(host, blocked) {
			continue
		}
		if len(allowed) > 0 && !matchesAny(host, allowed) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
}

// matchesAny reports whether host equals or is a subdomain of any domain.
func matchesAny(host string, domains []string) bool {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(d, "www.")))
		if d == "" {
			continue
		}
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}
