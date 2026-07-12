package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearchParsesBraveResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "k123" {
			t.Errorf("token header = %q", got)
		}
		if got := r.URL.Query().Get("q"); got != "eu treaties" {
			t.Errorf("q = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"EUR-Lex","url":"https://eur-lex.europa.eu/x","description":"Treaty texts","age":"3 days ago"},
			{"title":"Wiki","url":"https://en.wikipedia.org/y","description":"Overview"}
		]}}`))
	}))
	defer srv.Close()

	c := New("k123", srv.URL)
	res, err := c.Search(context.Background(), "eu treaties", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Title != "EUR-Lex" || res[0].Age != "3 days ago" || res[1].URL != "https://en.wikipedia.org/y" {
		t.Fatalf("unexpected results: %+v", res)
	}
}

func TestSearchUnconfigured(t *testing.T) {
	t.Parallel()
	if _, err := New("", "").Search(context.Background(), "x", 5); err == nil {
		t.Fatal("expected error without api key")
	}
	if New("", "").Configured() {
		t.Fatal("Configured() should be false without key")
	}
}

func TestFilterDomains(t *testing.T) {
	t.Parallel()
	rs := []Result{
		{URL: "https://www.example.com/a"},
		{URL: "https://sub.example.com/b"},
		{URL: "https://other.org/c"},
		{URL: "https://evil.net/d"},
	}
	got := FilterDomains(rs, []string{"example.com"}, nil)
	if len(got) != 2 {
		t.Fatalf("allowed filter: got %d results, want 2", len(got))
	}
	got = FilterDomains(rs, nil, []string{"evil.net", "other.org"})
	if len(got) != 2 {
		t.Fatalf("blocked filter: got %d results, want 2", len(got))
	}
	got = FilterDomains(rs, []string{"example.com"}, []string{"sub.example.com"})
	if len(got) != 1 || got[0].URL != "https://www.example.com/a" {
		t.Fatalf("combined filter: %+v", got)
	}
	if got := FilterDomains(rs, nil, nil); len(got) != 4 {
		t.Fatalf("no filter: got %d", len(got))
	}
}
