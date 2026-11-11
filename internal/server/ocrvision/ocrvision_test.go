package ocrvision

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ivoras/harlequin/internal/server/llm"
)

type fakeProvider struct{ fail bool }

func (fakeProvider) Name() string { return "fake" }
func (f fakeProvider) Chat(_ context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk, 2)
	if f.fail {
		ch <- llm.Chunk{Err: errors.New("boom")}
	} else {
		ch <- llm.Chunk{TextDelta: "page:" + string(req.Messages[0].Images[0].Data)}
		ch <- llm.Chunk{Done: true}
	}
	close(ch)
	return ch, nil
}

func render(data []byte, _, _ int) ([][]byte, int, error) {
	return [][]byte{[]byte("a"), []byte("bb"), []byte("c")}, 3, nil
}

// Pages are joined in order with their rune offsets; any page error fails the document.
func TestPDFJoinsPagesAndMapsOffsets(t *testing.T) {
	r := &Reader{Provider: fakeProvider{}, Model: "m", Render: render}
	text, starts, err := r.PDF(context.Background(), "x.pdf", nil)
	if err != nil || text != "page:a\n\npage:bb\n\npage:c" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if len(starts) != 3 || starts[0] != 0 || starts[1] != 8 || starts[2] != 17 {
		t.Fatalf("starts=%v", starts)
	}
	r.Provider = fakeProvider{fail: true}
	if _, _, err := r.PDF(context.Background(), "x.pdf", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v", err)
	}
}

func TestOrgContextAndPrompt(t *testing.T) {
	if OrgContext("Acme", "  ") != "" {
		t.Fatal("no description must give no context")
	}
	if got := OrgContext("Acme", "Računovodstveni servis."); got != "Organisation: Acme. Računovodstveni servis." {
		t.Fatalf("got %q", got)
	}
	var seen atomic.Value
	r := &Reader{Provider: promptSpy{&seen}, Model: "m", Context: "CTX", Render: render}
	if _, _, err := r.PDF(context.Background(), "x.pdf", nil); err != nil || !strings.HasPrefix(seen.Load().(string), "CTX Transcribe") {
		t.Fatalf("prompt %q err=%v", seen.Load(), err)
	}
}

type promptSpy struct{ seen *atomic.Value }

func (promptSpy) Name() string { return "spy" }
func (p promptSpy) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	p.seen.Store(req.Messages[0].Content)
	return fakeProvider{}.Chat(ctx, req)
}
