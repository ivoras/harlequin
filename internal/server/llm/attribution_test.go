package llm

import (
	"context"
	"testing"
)

func TestRoutingRecordsAttributedUsage(t *testing.T) {
	var got Attribution
	var gotUsage Usage
	r := &RoutingProvider{recordUsage: func(ctx context.Context, provider, model string, u Usage) {
		got, _ = AttributionFrom(ctx)
		gotUsage = u
	}}
	in := make(chan Chunk, 2)
	in <- Chunk{TextDelta: "hi"}
	in <- Chunk{Done: true, Usage: &Usage{PromptTokens: 9}}
	close(in)
	want := Attribution{UserID: 1, ProjectID: 2, SessionID: 3}
	for range r.wrap(WithAttribution(context.Background(), want), in) {
	}
	if got != want || gotUsage.PromptTokens != 9 {
		t.Fatalf("recorder got %+v / %+v", got, gotUsage)
	}
	if _, ok := AttributionFrom(context.Background()); ok {
		t.Fatal("attribution on a bare context")
	}
}
