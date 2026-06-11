package agent

import (
	"context"
	"log"
	"time"
)

// Background LLM gate: auto memory extraction, document distillation, and
// auto-titling all borrow the chat model outside live turns. The gate makes
// them run one at a time, start only while no live turn is using the LLM, and
// yield mid-flight — a live turn cancels the in-flight background completion
// immediately (see preemptBackgroundLLM) and the job restarts once the model
// is free again. Without this, a prompt sent right after a turn competes with
// that turn's extraction for the model (visible as interleaved slots on
// single-machine llama.cpp servers).

const (
	// bgPollInterval is how often a waiting background job re-checks whether the
	// LLM is still busy with a live turn.
	bgPollInterval = 500 * time.Millisecond
	// bgStartTimeout caps how long a background job waits (including preemption
	// retries) before being dropped, so queued goroutines cannot accumulate
	// without bound on a busy server.
	bgStartTimeout = 10 * time.Minute
	// bgMaxAttempts caps how many times a preempted job is restarted.
	bgMaxAttempts = 15
)

// localModeler is implemented by providers that can report whether the chat
// model runs on this machine (llm.OpenAICompatible, llm.RoutingProvider).
type localModeler interface{ Local() bool }

// gateEnabled reports whether background gating/preemption applies. Only a
// local model needs it: there, concurrent requests fight for the same GPU/CPU
// and slots. Hosted APIs handle parallel requests fine, so background work
// runs immediately. Providers that cannot say (tests, custom impls) are gated,
// the safe default.
func (a *Agent) gateEnabled() bool {
	if lp, ok := a.Provider.(localModeler); ok {
		return lp.Local()
	}
	return true
}

// preemptBackgroundLLM cancels the in-flight background LLM job, if any.
// Called at the start of every live turn so background work yields the model
// immediately rather than finishing its completion first.
func (a *Agent) preemptBackgroundLLM() {
	if c := a.bgCancel.Load(); c != nil {
		(*c)()
	}
}

// RunBackgroundLLM runs job while holding the single background-LLM slot.
// Jobs are serialized, start only while no live turn is using the LLM, and are
// cancelled (via job's ctx) the moment a live turn begins — the job is then
// re-run from scratch once the model is free, so jobs must be restartable.
// Returns false if the job never ran to completion: the slot or a free LLM did
// not materialize before ctx or bgStartTimeout expired, or the job was
// preempted bgMaxAttempts times. Exported so server-level background work that
// borrows the chat model (e.g. the cross-scope memory sweep) shares the gate.
func (a *Agent) RunBackgroundLLM(ctx context.Context, job func(ctx context.Context)) bool {
	if !a.gateEnabled() {
		job(ctx)
		return true
	}
	a.bgOnce.Do(func() { a.bgSlot = make(chan struct{}, 1) })
	ctx, cancel := context.WithTimeout(ctx, bgStartTimeout)
	defer cancel()
	select {
	case a.bgSlot <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-a.bgSlot }()
	for attempt := 0; attempt < bgMaxAttempts; attempt++ {
		// Stay off the model until no live turn is using it.
		for !a.llmFree() {
			select {
			case <-time.After(bgPollInterval):
			case <-ctx.Done():
				return false
			}
		}
		jobCtx, cancelJob := context.WithCancel(ctx)
		a.bgCancel.Store(&cancelJob)
		// A turn may have begun between the llmFree check and publishing the
		// cancel hook, and such a turn's preempt call can be missed — re-check.
		if !a.llmFree() {
			a.bgCancel.Store(nil)
			cancelJob()
			continue
		}
		job(jobCtx)
		a.bgCancel.Store(nil)
		preempted := jobCtx.Err() != nil && ctx.Err() == nil
		cancelJob()
		if !preempted {
			// Completed, or the overall deadline expired mid-job (-> false).
			return ctx.Err() == nil
		}
		log.Printf("background llm: job preempted by live turn (attempt %d/%d)", attempt+1, bgMaxAttempts)
	}
	return false
}
