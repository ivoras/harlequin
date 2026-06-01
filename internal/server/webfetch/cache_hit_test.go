package webfetch

import (
	"context"
	"testing"
	"time"
)

// Proves Fetch returns a cached result without touching the network: the URL
// host is unroutable, so a cache miss would error; a hit returns the seeded value.
func TestFetchUsesCacheNoNetwork(t *testing.T) {
	c := New(Options{})
	const url = "https://this-host-does-not-exist.invalid/page"
	c.cachePut(url, Result{Markdown: "CACHED", FinalURL: url, Title: "T"})

	start := time.Now()
	res, err := c.Fetch(context.Background(), url)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected cache hit, got error: %v", err)
	}
	if res.Markdown != "CACHED" {
		t.Fatalf("got %q, want CACHED", res.Markdown)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("cache hit took %v (network was likely hit)", elapsed)
	}
	t.Logf("cache hit returned in %v without network", elapsed)
}

// Proves entries past the 15-minute TTL are not served (and are swept).
func TestCacheTTLBoundary(t *testing.T) {
	c := New(Options{})
	const url = "https://example.com/x"

	// Just inside the TTL: served.
	c.mu.Lock()
	c.cache[url] = cacheEntry{res: Result{Markdown: "fresh"}, expires: time.Now().Add(cacheTTL - time.Second)}
	c.mu.Unlock()
	if _, ok := c.cacheGet(url); !ok {
		t.Fatal("entry within 15m TTL should be served")
	}

	// Just past the TTL: dropped.
	c.mu.Lock()
	c.cache[url] = cacheEntry{res: Result{Markdown: "stale"}, expires: time.Now().Add(-time.Second)}
	c.mu.Unlock()
	if _, ok := c.cacheGet(url); ok {
		t.Fatal("expired entry should not be served")
	}
	c.mu.Lock()
	_, present := c.cache[url]
	c.mu.Unlock()
	if present {
		t.Fatal("expired entry should be swept from the map")
	}
}

func TestCacheTTLIs15Minutes(t *testing.T) {
	if cacheTTL != 15*time.Minute {
		t.Fatalf("cacheTTL = %v, want 15m", cacheTTL)
	}
}
