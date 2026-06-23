// Package webfetch retrieves web pages with browser-like anti-bot measures
// (realistic Chrome headers, a uTLS JA3 fingerprint, HTTP/2, a cookie jar,
// redirect following, and small request jitter — no headless browser or CAPTCHA
// solving) and converts the resulting HTML to Markdown. It backs the agent's
// WebFetch tool.
package webfetch

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
)

const (
	// cacheTTL is the self-cleaning per-URL cache lifetime.
	cacheTTL = 15 * time.Minute
	// maxBodyBytes caps how much of a response we read/convert.
	maxBodyBytes = 5 << 20 // 5 MiB
	// requestTimeout bounds a single Fetch (including redirects).
	requestTimeout = 30 * time.Second
	// zyteEndpoint is the Zyte API extract endpoint.
	zyteEndpoint = "https://api.zyte.com/v1/extract"
	// zyteTimeout bounds a Zyte call (browser rendering is slower than a GET).
	zyteTimeout = 90 * time.Second
	// maxZyteResponse caps the JSON response read from Zyte (rendered HTML can be
	// large; allow more than maxBodyBytes for the JSON envelope + HTML).
	maxZyteResponse = 16 << 20 // 16 MiB
)

// Result is a fetched, converted page.
type Result struct {
	// Markdown is the page converted to Markdown (or decoded text for non-HTML).
	Markdown string
	// FinalURL is the URL after following redirects.
	FinalURL string
	// Title is the page <title>, when present.
	Title string
	// Cached is true when this result was served from the in-memory cache.
	Cached bool
	// ViaZyte is true when the page was fetched through the Zyte API fallback.
	ViaZyte bool
}

// RawResult is a fetched page before any Markdown conversion: the decoded
// (decompressed) response body plus enough metadata to parse or convert it. It
// backs DOM parsing and the JS sandbox fetch(), which need raw HTML, not Markdown.
type RawResult struct {
	// Body is the decoded, decompressed response body.
	Body []byte
	// ContentType is the response Content-Type header, lowercased.
	ContentType string
	// FinalURL is the URL after following redirects.
	FinalURL string
	// Status is the HTTP status code.
	Status int
	// Cached is true when this result was served from the in-memory cache.
	Cached bool
	// ViaZyte is true when this body came from the Zyte API fallback.
	ViaZyte bool
}

type cacheEntry struct {
	raw     RawResult
	expires time.Time
}

// Client fetches and converts web pages.
type Client struct {
	http *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry

	// Zyte fallback: when zyteKey is set, a direct fetch that returns HTTP 4xx is
	// retried through the Zyte browserHtml API, and the URL's domain is remembered
	// (zyteDomains) so subsequent fetches of that domain skip straight to Zyte.
	zyteKey     string
	zyteHTTP    *http.Client
	zmu         sync.Mutex
	zyteDomains map[string]bool
}

// Options configures a Client.
type Options struct {
	// AllowPrivate permits requests to loopback/private/link-local addresses.
	// Off by default as an SSRF guard (blocks cloud metadata, localhost, LAN).
	AllowPrivate bool
	// ZyteAPIKey enables the Zyte API fallback (https://docs.zyte.com). Empty
	// disables it — fetches then only use the normal browser-like path.
	ZyteAPIKey string
}

// New constructs a Client. The cookie jar persists Cloudflare clearance cookies
// across redirects within a single logical fetch.
func New(opts Options) *Client {
	jar, _ := cookiejar.New(nil)
	transport := newUTLSTransport(opts.AllowPrivate)
	c := &Client{
		http: &http.Client{
			Transport: transport,
			Jar:       jar,
			Timeout:   requestTimeout,
			// Follow all redirects (cap the chain to avoid loops), re-applying
			// our browser headers on every hop.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return nil
			},
		},
		cache: map[string]cacheEntry{},
	}
	if opts.ZyteAPIKey != "" {
		c.zyteKey = opts.ZyteAPIKey
		// Zyte is a normal public HTTPS API; use a plain client with a longer
		// timeout (browser rendering takes longer than a GET).
		c.zyteHTTP = &http.Client{Timeout: zyteTimeout}
		c.zyteDomains = map[string]bool{}
	}
	return c
}

// Fetch retrieves url (upgrading http→https), follows redirects, and returns the
// page as Markdown. Results are cached for 15 minutes per URL.
func (c *Client) Fetch(ctx context.Context, url string) (Result, error) {
	raw, err := c.FetchRaw(ctx, url)
	if err != nil {
		return Result{}, err
	}
	res := Result{FinalURL: raw.FinalURL, Cached: raw.Cached, ViaZyte: raw.ViaZyte}
	if strings.Contains(raw.ContentType, "html") || looksLikeHTML(raw.Body) {
		md, title := htmlToMarkdown(raw.Body, raw.FinalURL)
		res.Markdown = md
		res.Title = title
	} else {
		// Non-HTML (plain text, JSON, etc.): return decoded text as-is.
		res.Markdown = string(raw.Body)
	}
	return res, nil
}

// FetchRaw retrieves url (upgrading http→https), follows redirects, and returns
// the decoded response body without Markdown conversion. Results are cached for
// 15 minutes per URL (shared with Fetch).
func (c *Client) FetchRaw(ctx context.Context, url string) (RawResult, error) {
	url = upgradeScheme(strings.TrimSpace(url))
	if url == "" {
		return RawResult{}, errors.New("empty url")
	}

	if r, ok := c.cacheGet(url); ok {
		r.Cached = true
		return r, nil
	}

	// If this domain has already been seen to require Zyte (a previous direct
	// fetch 4xx'd and Zyte succeeded), skip the normal path entirely.
	host := hostOf(url)
	if c.zyteEnabled() && c.domainNeedsZyte(host) {
		raw, err := c.fetchViaZyte(ctx, url)
		if err != nil {
			return RawResult{}, err
		}
		c.cachePut(url, raw)
		return raw, nil
	}

	// Small random delay to avoid a too-regular request cadence.
	jitter(ctx, 120*time.Millisecond, 480*time.Millisecond)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return RawResult{}, err
	}
	setBrowserHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return RawResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		// A 4xx often means anti-bot/geo/login walls our direct fetch can't pass.
		// If Zyte is configured, retry through its browser API and remember that
		// this domain needs Zyte so future fetches skip straight to it.
		if c.zyteEnabled() && resp.StatusCode < 500 {
			raw, zerr := c.fetchViaZyte(ctx, url)
			if zerr == nil {
				c.markDomainNeedsZyte(host)
				c.cachePut(url, raw)
				return raw, nil
			}
			return RawResult{}, fmt.Errorf("server returned %s; zyte fallback failed: %w", resp.Status, zerr)
		}
		return RawResult{}, fmt.Errorf("server returned %s", resp.Status)
	}

	body, err := decodeBody(resp)
	if err != nil {
		return RawResult{}, err
	}

	finalURL := url
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	raw := RawResult{
		Body:        body,
		ContentType: strings.ToLower(resp.Header.Get("Content-Type")),
		FinalURL:    finalURL,
		Status:      resp.StatusCode,
	}
	c.cachePut(url, raw)
	return raw, nil
}

// zyteEnabled reports whether the Zyte fallback is configured.
func (c *Client) zyteEnabled() bool { return c.zyteKey != "" }

// hostOf extracts the lowercased host of a URL (the Zyte-domain cache key).
func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (c *Client) domainNeedsZyte(host string) bool {
	if host == "" {
		return false
	}
	c.zmu.Lock()
	defer c.zmu.Unlock()
	return c.zyteDomains[host]
}

func (c *Client) markDomainNeedsZyte(host string) {
	if host == "" {
		return
	}
	c.zmu.Lock()
	c.zyteDomains[host] = true
	c.zmu.Unlock()
}

// fetchViaZyte retrieves url through the Zyte browserHtml API and returns it as a
// RawResult (status 200, text/html). The API key is the HTTP Basic username.
func (c *Client) fetchViaZyte(ctx context.Context, url string) (RawResult, error) {
	reqBody, _ := json.Marshal(map[string]any{"url": url, "browserHtml": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, zyteEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return RawResult{}, err
	}
	req.SetBasicAuth(c.zyteKey, "")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.zyteHTTP.Do(req)
	if err != nil {
		return RawResult{}, fmt.Errorf("zyte request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxZyteResponse))
	if err != nil {
		return RawResult{}, fmt.Errorf("zyte read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return RawResult{}, fmt.Errorf("zyte returned %s: %s", resp.Status, snippet(respBody))
	}

	var out struct {
		URL         string `json:"url"`
		BrowserHTML string `json:"browserHtml"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return RawResult{}, fmt.Errorf("zyte decode: %w", err)
	}
	if strings.TrimSpace(out.BrowserHTML) == "" {
		return RawResult{}, errors.New("zyte returned empty browserHtml")
	}
	final := url
	if out.URL != "" {
		final = out.URL
	}
	return RawResult{
		Body:        []byte(out.BrowserHTML),
		ContentType: "text/html",
		FinalURL:    final,
		Status:      http.StatusOK,
		ViaZyte:     true,
	}, nil
}

// snippet returns a short, single-line excerpt for error messages.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func (c *Client) cacheGet(url string) (RawResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	// Self-clean: drop every expired entry on access.
	for k, e := range c.cache {
		if now.After(e.expires) {
			delete(c.cache, k)
		}
	}
	if e, ok := c.cache[url]; ok && now.Before(e.expires) {
		return e.raw, true
	}
	return RawResult{}, false
}

func (c *Client) cachePut(url string, raw RawResult) {
	c.mu.Lock()
	c.cache[url] = cacheEntry{raw: raw, expires: time.Now().Add(cacheTTL)}
	c.mu.Unlock()
}

// upgradeScheme upgrades http:// to https:// and prepends https:// to a bare host.
func upgradeScheme(url string) string {
	switch {
	case strings.HasPrefix(url, "http://"):
		return "https://" + strings.TrimPrefix(url, "http://")
	case strings.HasPrefix(url, "https://"):
		return url
	case url == "":
		return ""
	default:
		return "https://" + url
	}
}

// setBrowserHeaders applies a consistent, current-Chrome header set. Versions in
// the User-Agent and the sec-ch-ua hints are kept in sync to avoid the kind of
// header mismatch bot detectors flag.
func setBrowserHeaders(req *http.Request) {
	h := req.Header
	set := func(k, v string) {
		if h.Get(k) == "" {
			h.Set(k, v)
		}
	}
	set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	set("Accept-Language", "en-US,en;q=0.9")
	set("Accept-Encoding", "gzip, deflate, br")
	set("sec-ch-ua", `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`)
	set("sec-ch-ua-mobile", "?0")
	set("sec-ch-ua-platform", `"Windows"`)
	set("Sec-Fetch-Dest", "document")
	set("Sec-Fetch-Mode", "navigate")
	set("Sec-Fetch-Site", "none")
	set("Sec-Fetch-User", "?1")
	set("Upgrade-Insecure-Requests", "1")
	set("Cache-Control", "max-age=0")
	set("Priority", "u=0, i")
}

// decodeBody reads and decompresses the response body per Content-Encoding,
// capping the amount read.
func decodeBody(resp *http.Response) ([]byte, error) {
	limited := io.LimitReader(resp.Body, maxBodyBytes)
	var reader io.Reader = limited
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		gz, err := gzip.NewReader(limited)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	case "deflate":
		fr := flate.NewReader(limited)
		defer fr.Close()
		reader = fr
	case "br":
		reader = brotli.NewReader(limited)
	}
	return io.ReadAll(io.LimitReader(reader, maxBodyBytes))
}

func looksLikeHTML(b []byte) bool {
	head := bytes.ToLower(bytes.TrimSpace(b))
	if len(head) > 512 {
		head = head[:512]
	}
	return bytes.Contains(head, []byte("<html")) || bytes.Contains(head, []byte("<!doctype html")) || bytes.Contains(head, []byte("<head")) || bytes.Contains(head, []byte("<body"))
}

func jitter(ctx context.Context, min, max time.Duration) {
	d := min + time.Duration(rand.Int63n(int64(max-min)+1))
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// privateIP reports whether ip is loopback, link-local, private, or otherwise
// not a routable public address (used for the SSRF guard).
func privateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// Carrier-grade NAT 100.64.0.0/10.
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true
	}
	return false
}
