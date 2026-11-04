package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsTransient(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"503":        {&HTTPStatusError{Code: 503}, true},
		"429":        {&HTTPStatusError{Code: 429}, true},
		"400":        {&HTTPStatusError{Code: 400}, false},
		"401":        {&HTTPStatusError{Code: 401}, false},
		"canceled":   {context.Canceled, false},
		"h2 reset":   {errors.New("stream error: stream ID 323; INTERNAL_ERROR; received from peer"), true},
		"unexpected": {io.ErrUnexpectedEOF, true},
		"refused":    {errors.New("dial tcp: connection refused"), true},
		"other":      {errors.New("tool exploded"), false},
		"nil":        {nil, false},
	}
	for name, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("%s: IsTransient = %v, want %v", name, got, c.want)
		}
	}
}

// fakeGateway serves a chat endpoint where models in failing return status
// `code`; others stream "ok".
func fakeGateway(t *testing.T, code int, failing ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, f := range failing {
			if req.Model == f {
				http.Error(w, "down", code)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", req.Model)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func drain(t *testing.T, ch <-chan Chunk) (text, model string) {
	t.Helper()
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		text += c.TextDelta
		if c.Model != "" {
			model = c.Model
		}
	}
	return
}

func newTestRouter(url, fallback string) *RoutingProvider {
	RetryBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	p := NewOpenAICompatible("p", url, "", "primary")
	r := NewRoutingProvider(map[string]*OpenAICompatible{"p": p}, "p", []string{"p"}, nil, map[string]int{"fb": 1000}, nil)
	r.SetFallbackModel(fallback)
	return r
}

func TestRouterRetriesThenFallsBackToModel(t *testing.T) {
	srv, calls := fakeGateway(t, 503, "primary")
	r := newTestRouter(srv.URL, "fb")
	ch, err := r.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	text, model := drain(t, ch)
	if text != "ok" || model != "fb" {
		t.Fatalf("got %q from %q, want ok from fb", text, model)
	}
	if n := calls.Load(); n != 4 { // 3 primary attempts + 1 fallback
		t.Errorf("gateway calls = %d, want 4", n)
	}
	// Primary is now marked down: the next request goes straight to the fallback.
	calls.Store(0)
	ch, _ = r.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	drain(t, ch)
	if n := calls.Load(); n != 1 {
		t.Errorf("calls while primary marked down = %d, want 1", n)
	}
}

func TestRouterNoFallbackForRejectedRequest(t *testing.T) {
	srv, calls := fakeGateway(t, 400, "primary")
	r := newTestRouter(srv.URL, "fb")
	if _, err := r.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}}); err == nil {
		t.Fatal("want an error for a rejected request")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("a 400 must not be retried or fall back; calls = %d", n)
	}
}

func TestRouterFallbackSkippedWhenPromptTooLong(t *testing.T) {
	srv, _ := fakeGateway(t, 503, "primary")
	r := newTestRouter(srv.URL, "fb") // fb window = 1000 tokens
	long := make([]byte, 20000)
	for i := range long {
		long[i] = 'a'
	}
	_, err := r.Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: string(long)}}})
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestRouterUseFallbackSkipsPrimary(t *testing.T) {
	srv, calls := fakeGateway(t, 503, "primary")
	r := newTestRouter(srv.URL, "fb")
	ch, err := r.Chat(context.Background(), ChatRequest{UseFallback: true, Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, model := drain(t, ch); model != "fb" || calls.Load() != 1 {
		t.Errorf("model=%q calls=%d, want fb and 1", model, calls.Load())
	}
}
