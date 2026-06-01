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
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
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
}

type cacheEntry struct {
	res     Result
	expires time.Time
}

// Client fetches and converts web pages.
type Client struct {
	http *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// Options configures a Client.
type Options struct {
	// AllowPrivate permits requests to loopback/private/link-local addresses.
	// Off by default as an SSRF guard (blocks cloud metadata, localhost, LAN).
	AllowPrivate bool
}

// New constructs a Client. The cookie jar persists Cloudflare clearance cookies
// across redirects within a single logical fetch.
func New(opts Options) *Client {
	jar, _ := cookiejar.New(nil)
	transport := newUTLSTransport(opts.AllowPrivate)
	return &Client{
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
}

// Fetch retrieves url (upgrading http→https), follows redirects, and returns the
// page as Markdown. Results are cached for 15 minutes per URL.
func (c *Client) Fetch(ctx context.Context, url string) (Result, error) {
	url = upgradeScheme(strings.TrimSpace(url))
	if url == "" {
		return Result{}, errors.New("empty url")
	}

	if r, ok := c.cacheGet(url); ok {
		r.Cached = true
		return r, nil
	}

	// Small random delay to avoid a too-regular request cadence.
	jitter(ctx, 120*time.Millisecond, 480*time.Millisecond)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}
	setBrowserHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("server returned %s", resp.Status)
	}

	body, err := decodeBody(resp)
	if err != nil {
		return Result{}, err
	}

	finalURL := url
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	res := Result{FinalURL: finalURL}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "html") || looksLikeHTML(body) {
		md, title := htmlToMarkdown(body, finalURL)
		res.Markdown = md
		res.Title = title
	} else {
		// Non-HTML (plain text, JSON, etc.): return decoded text as-is.
		res.Markdown = string(body)
	}

	c.cachePut(url, res)
	return res, nil
}

func (c *Client) cacheGet(url string) (Result, bool) {
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
		return e.res, true
	}
	return Result{}, false
}

func (c *Client) cachePut(url string, res Result) {
	c.mu.Lock()
	c.cache[url] = cacheEntry{res: res, expires: time.Now().Add(cacheTTL)}
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
