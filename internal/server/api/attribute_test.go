package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ivoras/harlequin/internal/server/llm"
)

// Without an authenticated user there is nothing to attribute; an existing,
// more specific attribution is left alone.
func TestAttributeRequestKeepsExisting(t *testing.T) {
	var got llm.Attribution
	var ok bool
	h := attributeRequest(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = llm.AttributionFrom(r.Context())
	}))
	r := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if ok {
		t.Fatalf("attributed an anonymous request: %+v", got)
	}
	r = r.WithContext(llm.WithAttribution(r.Context(), llm.Attribution{UserID: 9, ProjectID: 3}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !ok || got.UserID != 9 || got.ProjectID != 3 {
		t.Fatalf("overwrote attribution: %+v", got)
	}
}
