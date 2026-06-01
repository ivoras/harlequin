package webfetch

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveFetch(t *testing.T) {
	if os.Getenv("WEBFETCH_LIVE") == "" {
		t.Skip("set WEBFETCH_LIVE=1 to run")
	}
	c := New(Options{})
	for _, u := range []string{"http://example.com", "https://www.cloudflare.com/", "https://news.ycombinator.com/"} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		res, err := c.Fetch(ctx, u)
		cancel()
		if err != nil {
			t.Errorf("%s -> ERROR: %v", u, err)
			continue
		}
		md := res.Markdown
		if len(md) > 140 {
			md = md[:140]
		}
		t.Logf("%s -> final=%s title=%q len=%d\n   %s", u, res.FinalURL, res.Title, len(res.Markdown), md)
	}
}
