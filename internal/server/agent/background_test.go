package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivoras/harlequin/internal/server/llm"
)

// remoteProvider is a Provider stub whose model is not local: the background
// gate must become a pass-through for it.
type remoteProvider struct{}

func (remoteProvider) Name() string { return "remote" }
func (remoteProvider) Chat(ctx context.Context, req llm.ChatRequest) (<-chan llm.Chunk, error) {
	ch := make(chan llm.Chunk)
	close(ch)
	return ch, nil
}
func (remoteProvider) Local() bool { return false }

// With a remote (hosted) model there is no slot contention, so background jobs
// run immediately — no serialization, no yielding to live turns.
func TestRunBackgroundLLMBypassedForRemoteModel(t *testing.T) {
	a := &Agent{Provider: remoteProvider{}}
	a.inFlight.Add(1) // live turn in flight must not delay the job
	defer a.inFlight.Add(-1)
	ran := false
	done := make(chan struct{})
	go func() {
		a.RunBackgroundLLM(context.Background(), func(context.Context) { ran = true })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background job was gated despite a remote model")
	}
	if !ran {
		t.Error("job did not run")
	}
}

// Background jobs must never run concurrently with each other.
func TestRunBackgroundLLMSerializes(t *testing.T) {
	a := &Agent{}
	var running atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !a.RunBackgroundLLM(context.Background(), func(context.Context) {
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
		a.RunBackgroundLLM(context.Background(), func(context.Context) {})
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
	if a.RunBackgroundLLM(ctx, func(context.Context) { t.Error("job ran despite expired context") }) {
		t.Error("runBackgroundLLM reported success for a skipped job")
	}
}

// A live turn starting mid-job must cancel the job's context immediately, and
// the job must be retried once the turn ends.
func TestRunBackgroundLLMPreemptedByTurnAndRetried(t *testing.T) {
	a := &Agent{}
	var attempts atomic.Int64
	started := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		result <- a.RunBackgroundLLM(context.Background(), func(ctx context.Context) {
			if attempts.Add(1) == 1 {
				close(started)
				// Simulate an in-flight completion: block until preempted.
				select {
				case <-ctx.Done():
				case <-time.After(10 * time.Second):
					t.Error("first attempt was never preempted")
				}
			}
		})
	}()
	<-started
	// A live turn begins: turn() bumps inFlight, then preempts background work.
	a.inFlight.Add(1)
	a.preemptBackgroundLLM()
	time.Sleep(2 * bgPollInterval) // job should now be waiting for a free model
	a.inFlight.Add(-1)
	select {
	case ok := <-result:
		if !ok {
			t.Error("preempted job did not eventually complete")
		}
	case <-time.After(10 * bgPollInterval):
		t.Fatal("preempted job was not retried after the turn ended")
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("job ran %d times, want 2 (preempted once, retried once)", n)
	}
}
