package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Background jobs must never run concurrently with each other.
func TestRunBackgroundLLMSerializes(t *testing.T) {
	a := &Agent{}
	var running atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !a.runBackgroundLLM(context.Background(), func() {
				if n := running.Add(1); n > 1 {
					t.Errorf("%d background jobs running concurrently", n)
				}
				time.Sleep(20 * time.Millisecond)
				running.Add(-1)
			}) {
				t.Error("background job was skipped")
			}
		}()
	}
	wg.Wait()
}

// A background job must not start while a live turn is in flight, and must run
// once the turn ends.
func TestRunBackgroundLLMYieldsToLiveTurn(t *testing.T) {
	a := &Agent{}
	a.inFlight.Add(1)
	done := make(chan struct{})
	go func() {
		a.runBackgroundLLM(context.Background(), func() {})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("background job ran while a turn was in flight")
	case <-time.After(3 * bgPollInterval):
	}
	a.inFlight.Add(-1)
	select {
	case <-done:
	case <-time.After(10 * bgPollInterval):
		t.Fatal("background job did not run after the turn ended")
	}
}

// A job whose context expires while waiting is skipped, not run late.
func TestRunBackgroundLLMSkipsOnContextDone(t *testing.T) {
	a := &Agent{}
	a.inFlight.Add(1)
	defer a.inFlight.Add(-1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if a.runBackgroundLLM(ctx, func() { t.Error("job ran despite expired context") }) {
		t.Error("runBackgroundLLM reported success for a skipped job")
	}
}
