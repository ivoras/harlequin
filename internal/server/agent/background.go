package agent

import (
	"context"
	"time"
)

// Background LLM gate: auto memory extraction, document distillation, and
// auto-titling all borrow the chat model outside live turns. The gate makes
// them run one at a time and only start while no live turn is using the LLM,
// so an interactive turn never finds the model already busy with overlapping
// background completions (noticeable on single-slot local servers).

const (
	// bgPollInterval is how often a waiting background job re-checks whether the
	// LLM is still busy with a live turn.
	bgPollInterval = 500 * time.Millisecond
	// bgStartTimeout caps how long a background job waits for its slot and a free
	// LLM, so queued goroutines cannot accumulate without bound on a busy server.
	bgStartTimeout = 10 * time.Minute
)

// runBackgroundLLM runs job while holding the single background-LLM slot. It
// first waits for the slot (one background job at a time), then for the LLM to
// be free of live turns. A live turn that begins after job has started is not
// interrupted; the overlap is bounded by one background completion. Returns
// false if the slot or a free LLM did not materialize before ctx or
// bgStartTimeout expired — the job is then skipped, not queued.
func (a *Agent) runBackgroundLLM(ctx context.Context, job func()) bool {
	a.bgOnce.Do(func() { a.bgSlot = make(chan struct{}, 1) })
	ctx, cancel := context.WithTimeout(ctx, bgStartTimeout)
	defer cancel()
	select {
	case a.bgSlot <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-a.bgSlot }()
	// Hold the slot but stay off the model until no live turn is using it. (A
	// turn can still start between this check and the job's first request; that
	// small race is inherent — turns are never made to wait on background work.)
	for !a.llmFree() {
		select {
		case <-time.After(bgPollInterval):
		case <-ctx.Done():
			return false
		}
	}
	job()
	return true
}
