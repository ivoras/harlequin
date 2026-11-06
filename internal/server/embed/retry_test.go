package embed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbedRetriesTransient(t *testing.T) {
	old := embedBackoff
	embedBackoff = time.Millisecond
	defer func() { embedBackoff = old }()

	run := func(status int, failures int32) (calls int32, err error) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) <= failures {
				http.Error(w, "boom", status)
				return
			}
			w.Write([]byte(`{"data":[{"index":0,"embedding":[1]}]}`))
		}))
		defer srv.Close()
		_, err = New(srv.URL, "", "m", 1, "", "").Embed(context.Background(), []string{"x"})
		return n.Load(), err
	}

	if calls, err := run(429, 2); err != nil || calls != 3 {
		t.Errorf("429 x2 then ok: calls=%d err=%v, want 3 calls and success", calls, err)
	}
	if calls, err := run(500, 99); err == nil || calls != embedRetries+1 {
		t.Errorf("persistent 500: calls=%d err=%v, want %d calls and an error", calls, err, embedRetries+1)
	}
	if calls, err := run(400, 99); err == nil || calls != 1 {
		t.Errorf("400 must not be retried: calls=%d err=%v", calls, err)
	}
}
