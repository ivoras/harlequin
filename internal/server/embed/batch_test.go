package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEmbedBatching checks that large input sets split into ≤maxEmbedBatch
// requests and reassemble in order (one giant request used to blow the client
// timeout on book-sized documents).
func TestEmbedBatching(t *testing.T) {
	t.Parallel()
	var calls, total int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls++
		if len(req.Input) > maxEmbedBatch {
			t.Errorf("request %d carries %d inputs (max %d)", calls, len(req.Input), maxEmbedBatch)
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Data []datum `json:"data"`
		}{}
		for i := range req.Input {
			// Encode the global order in the vector so reassembly is checkable.
			out.Data = append(out.Data, datum{Index: i, Embedding: []float32{float32(total + i)}})
		}
		total += len(req.Input)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()

	e := New(srv.URL, "", "test-model", 1, "", "")
	n := maxEmbedBatch*2 + 7 // forces 3 requests
	inputs := make([]string, n)
	for i := range inputs {
		inputs[i] = fmt.Sprintf("sentence %d", i)
	}
	vecs, err := e.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != n {
		t.Fatalf("got %d vectors, want %d", len(vecs), n)
	}
	if calls != 3 {
		t.Errorf("got %d requests, want 3", calls)
	}
	for i, v := range vecs {
		if len(v) != 1 || v[0] != float32(i) {
			t.Fatalf("vector %d out of order: %v", i, v)
		}
	}
}
