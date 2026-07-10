package llm

import (
	"sync"
	"time"
)

const (
	// ppWindow is how many recent calls feed the rolling prompt-processing average.
	ppWindow = 10
	// ppTimeoutMultiplier caps a call's timeout at this many times its predicted
	// prompt-processing time.
	ppTimeoutMultiplier = 10
	// minPPSampleTokens ignores tiny (mostly cache-hit) prefills whose rate is
	// noisy and would skew the average.
	minPPSampleTokens = 50

	// Guardrails around the dynamic timeout.
	defaultRequestTimeout = 5 * time.Minute // used until PP samples exist
	minRequestTimeout     = 60 * time.Second
	maxRequestTimeout     = 20 * time.Minute
)

// ppTracker keeps a rolling average of recent prompt-processing rates (tokens/s)
// over the last ppWindow calls. Safe for concurrent use.
type ppTracker struct {
	mu      sync.Mutex
	samples []float64 // ring of the last ppWindow rates
	next    int
}

func newPPTracker() *ppTracker { return &ppTracker{} }

func (t *ppTracker) add(rate float64) {
	if rate <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) < ppWindow {
		t.samples = append(t.samples, rate)
		return
	}
	t.samples[t.next] = rate
	t.next = (t.next + 1) % ppWindow
}

func (t *ppTracker) avg() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) == 0 {
		return 0
	}
	var sum float64
	for _, r := range t.samples {
		sum += r
	}
	return sum / float64(len(t.samples))
}

// requestTimeout returns the dynamic per-call timeout: ppTimeoutMultiplier times
// the predicted prompt-processing time (estimated prompt tokens / recent average
// PP rate), clamped to [minRequestTimeout, maxRequestTimeout]. Until enough
// samples exist it falls back to defaultRequestTimeout.
func (p *OpenAICompatible) requestTimeout(estTokens int) time.Duration {
	avg := p.ppAvg.avg()
	if avg <= 0 {
		return defaultRequestTimeout
	}
	predicted := float64(estTokens) / avg // seconds of predicted prompt processing
	timeout := time.Duration(ppTimeoutMultiplier * predicted * float64(time.Second))
	if timeout < minRequestTimeout {
		timeout = minRequestTimeout
	}
	if timeout > maxRequestTimeout {
		timeout = maxRequestTimeout
	}
	return timeout
}

// idleWatchdog cancels an in-flight call when the stream stays quiet for
// longer than the allowed gap; every touch() re-arms it. Used instead of a
// hard whole-call deadline so a long generation, which keeps producing
// chunks, is never killed — only a genuinely stalled request is.
type idleWatchdog struct {
	gap   time.Duration
	timer *time.Timer
	once  sync.Once     // touch() can race the timer and re-arm it; fire at most once
	fired chan struct{} // closed by the timer just before cancelling
}

func newIdleWatchdog(gap time.Duration, cancel func()) *idleWatchdog {
	w := &idleWatchdog{gap: gap, fired: make(chan struct{})}
	w.timer = time.AfterFunc(gap, func() {
		w.once.Do(func() {
			close(w.fired)
			cancel()
		})
	})
	return w
}

// touch re-arms the watchdog after stream activity.
func (w *idleWatchdog) touch() {
	if !w.expired() {
		w.timer.Reset(w.gap)
	}
}

// stop disarms the watchdog once the call is finished.
func (w *idleWatchdog) stop() { w.timer.Stop() }

// expired reports whether the watchdog cancelled the call.
func (w *idleWatchdog) expired() bool {
	select {
	case <-w.fired:
		return true
	default:
		return false
	}
}

// recordPP folds a completed call's prompt-processing rate into the rolling
// average, preferring server-reported timings (cache-accurate) over a wall-clock
// estimate from usage and the measured prefill (request start to first token).
func (p *OpenAICompatible) recordPP(usage *Usage, timings *Timings, start, firstContentAt time.Time) {
	var rate float64
	switch {
	case timings != nil && timings.PromptN >= minPPSampleTokens && timings.PromptMS > 0:
		rate = float64(timings.PromptN) / (timings.PromptMS / 1000)
	case usage != nil && !firstContentAt.IsZero():
		processed := usage.PromptTokens - usage.CachedPromptTokens()
		prefill := firstContentAt.Sub(start).Seconds()
		if processed >= minPPSampleTokens && prefill > 0 {
			rate = float64(processed) / prefill
		}
	}
	p.ppAvg.add(rate)
}
