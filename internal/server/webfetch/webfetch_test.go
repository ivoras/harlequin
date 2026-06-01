package webfetch

import (
	"bytes"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUpgradeScheme(t *testing.T) {
	cases := map[string]string{
		"http://example.com/x":  "https://example.com/x",
		"https://example.com/x": "https://example.com/x",
		"example.com/x":         "https://example.com/x",
		"":                      "",
	}
	for in, want := range cases {
		if got := upgradeScheme(in); got != want {
			t.Errorf("upgradeScheme(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	html := `<html><head><title> Hello World </title></head>
	<body>
	<h1>Heading</h1>
	<p>Some <strong>bold</strong> text with a <a href="/page">link</a>.</p>
	<script>var x = 1;</script>
	</body></html>`
	md, title := htmlToMarkdown([]byte(html), "https://example.com/dir/")
	if title != "Hello World" {
		t.Errorf("title = %q, want %q", title, "Hello World")
	}
	if !strings.Contains(md, "# Heading") {
		t.Errorf("missing heading in:\n%s", md)
	}
	if !strings.Contains(md, "**bold**") {
		t.Errorf("missing bold in:\n%s", md)
	}
	if !strings.Contains(md, "https://example.com/page") {
		t.Errorf("link not absolutized in:\n%s", md)
	}
	if strings.Contains(md, "var x = 1") {
		t.Errorf("script content leaked into markdown:\n%s", md)
	}
}

func TestPrivateIP(t *testing.T) {
	private := []string{"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.5.5", "169.254.1.1", "100.64.0.1", "::1", "fc00::1"}
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	for _, s := range private {
		if !privateIP(net.ParseIP(s)) {
			t.Errorf("privateIP(%s) = false, want true", s)
		}
	}
	for _, s := range public {
		if privateIP(net.ParseIP(s)) {
			t.Errorf("privateIP(%s) = true, want false", s)
		}
	}
}

func TestDecodeBodyGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("hello gzipped world"))
	_ = gz.Close()

	resp := &http.Response{
		Header: http.Header{"Content-Encoding": {"gzip"}},
		Body:   io.NopCloser(bytes.NewReader(buf.Bytes())),
	}
	got, err := decodeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello gzipped world" {
		t.Errorf("got %q", got)
	}
}

func TestDecodeBodyIdentity(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{},
		Body:   io.NopCloser(strings.NewReader("plain")),
	}
	got, err := decodeBody(resp)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plain" {
		t.Errorf("got %q", got)
	}
}

func TestCache(t *testing.T) {
	c := New(Options{})
	url := "https://example.com/x"
	if _, ok := c.cacheGet(url); ok {
		t.Fatal("unexpected cache hit")
	}
	c.cachePut(url, Result{Markdown: "md", FinalURL: url})
	if r, ok := c.cacheGet(url); !ok || r.Markdown != "md" {
		t.Fatalf("cache miss after put: %v %q", ok, r.Markdown)
	}
	// Force expiry.
	c.mu.Lock()
	c.cache[url] = cacheEntry{res: Result{Markdown: "md"}, expires: time.Now().Add(-time.Minute)}
	c.mu.Unlock()
	if _, ok := c.cacheGet(url); ok {
		t.Fatal("expired entry should not be returned")
	}
}
