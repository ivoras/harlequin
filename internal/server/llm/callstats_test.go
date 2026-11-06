package llm

import (
	"errors"
	"testing"
	"time"
)

func TestFormatCallStats(t *testing.T) {
	c := 0.00012
	u := &Usage{PromptTokens: 100, CompletionTokens: 7, Cost: &c}
	u.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 60}
	got := formatCallStats("chat", "m/x", u, 1500*time.Millisecond, 400*time.Millisecond, true, nil)
	want := "llm[chat]: done model=m/x prompt=100 cached=60 completion=7 ttft_ms=400 total_ms=1500 cost_usd=0.00012 ok=true"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	got = formatCallStats("chat", "m", nil, time.Second, 0, false, errors.New("x"))
	want = "llm[chat]: done model=m prompt=-1 cached=-1 completion=-1 ttft_ms=-1 total_ms=1000 cost_usd=-1 ok=false"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
