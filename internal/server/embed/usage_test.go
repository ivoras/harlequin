package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type usageCall struct {
	model     string
	tokens    int
	cost      *float64
	estimated bool
}

func usageServer(t *testing.T, usage string) *OpenAIEmbedder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]` + usage + `}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "", "m", 1, "", "")
}

// The provider's token count and cost are passed on; without them the tokens
// are estimated and flagged so the cost comes from the price table.
func TestEmbedReportsUsage(t *testing.T) {
	var got []usageCall
	rec := func(_ context.Context, model string, tokens int, cost *float64, est bool) {
		got = append(got, usageCall{model, tokens, cost, est})
	}

	e := usageServer(t, `,"usage":{"prompt_tokens":42,"total_tokens":42,"cost":0.0005}`)
	e.SetUsageFunc(rec)
	if _, err := e.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].tokens != 42 || got[0].estimated || got[0].cost == nil || *got[0].cost != 0.0005 || got[0].model != "m" {
		t.Fatalf("reported: %+v", got)
	}

	got = nil
	e = usageServer(t, ``)
	e.SetUsageFunc(rec)
	if _, err := e.EmbedQuery(context.Background(), []string{strings.Repeat("x", 400)}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].tokens != 100 || !got[0].estimated || got[0].cost != nil {
		t.Fatalf("estimated: %+v", got)
	}

	// No recorder: nothing to call, nothing breaks.
	e = usageServer(t, ``)
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
}
