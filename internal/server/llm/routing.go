package llm

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"
)

// primaryDownFor is how long, after every provider failed transiently, requests
// go straight to the fallback model instead of re-paying the retry delays.
const primaryDownFor = 30 * time.Second

// UsageRecorder is called after each completion with token usage. It is optional.
type UsageRecorder func(ctx context.Context, provider, model string, u Usage)

// Attribution says whose spend an LLM call is: the user (whose db records it)
// and, when it serves a session, which one. Carried on the request context so
// every call made on a session's behalf — turn steps, delegated sub-calls,
// post-turn memory extraction and its judges, titling — is recorded the same way.
type Attribution struct {
	UserID    int64
	ProjectID int64 // non-zero for a project session
	SessionID int64 // 0 = not tied to a session
}

type attributionKey struct{}

// WithAttribution returns ctx carrying a.
func WithAttribution(ctx context.Context, a Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, a)
}

// AttributionFrom returns the attribution on ctx, if any.
func AttributionFrom(ctx context.Context) (Attribution, bool) {
	a, ok := ctx.Value(attributionKey{}).(Attribution)
	return a, ok
}

// RoutingProvider wraps an ordered set of providers and implements routing +
// fallback. It selects a provider by per-model rules (or the default) and, on
// failure, falls back to the next provider in the configured order.
type RoutingProvider struct {
	providers       map[string]*OpenAICompatible
	defaultProvider string
	fallbackOrder   []string
	modelRules      map[string]string
	contextWindows  map[string]int
	recordUsage     UsageRecorder

	fallbackModel string       // routing.fallback_model; "" = none
	downUntil     atomic.Int64 // unix nanos until which the primary is considered down
}

// SetFallbackModel sets the model used, on the default provider, when every
// provider has failed.
func (r *RoutingProvider) SetFallbackModel(m string) { r.fallbackModel = m }

// HasFallback reports whether a fallback model is configured.
func (r *RoutingProvider) HasFallback() bool { return r.fallbackModel != "" }

// NewRoutingProvider builds a routing provider.
func NewRoutingProvider(providers map[string]*OpenAICompatible, defaultProvider string, fallbackOrder []string, modelRules map[string]string, contextWindows map[string]int, rec UsageRecorder) *RoutingProvider {
	cw := make(map[string]int, len(contextWindows))
	for k, v := range contextWindows {
		if v > 0 {
			cw[k] = v
		}
	}
	return &RoutingProvider{
		providers:       providers,
		defaultProvider: defaultProvider,
		fallbackOrder:   fallbackOrder,
		modelRules:      modelRules,
		contextWindows:  cw,
		recordUsage:     rec,
	}
}

// ContextMax resolves the context window for a model name; "" means the
// default provider's model (what an unpinned request is sent to).
func (r *RoutingProvider) ContextMax(model string) int {
	if p, ok := r.providers[r.defaultProvider]; ok && model == "" {
		model = p.Model()
	}
	return ContextMax(model, r.contextWindows)
}

// Name identifies the router.
func (r *RoutingProvider) Name() string { return "router" }

// Local reports whether the default provider — the one that serves agent turns
// and background jobs unless a model override routes elsewhere — runs on this
// machine. Used to decide whether background LLM work must be serialized
// behind live turns (see agent.RunBackgroundLLM).
func (r *RoutingProvider) Local() bool {
	if p, ok := r.providers[r.defaultProvider]; ok {
		return p.Local()
	}
	return false
}

// order returns the provider names to try, in priority order, for a request.
func (r *RoutingProvider) order(req ChatRequest) []string {
	var first string
	if req.Model != "" {
		if name, ok := r.modelRules[req.Model]; ok {
			first = name
		}
	}
	if first == "" {
		first = r.defaultProvider
	}

	seen := map[string]bool{}
	out := []string{}
	add := func(n string) {
		if n != "" && !seen[n] && r.providers[n] != nil {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(first)
	for _, n := range r.fallbackOrder {
		add(n)
	}
	for n := range r.providers {
		add(n)
	}
	return out
}

// Chat tries providers in order until one returns a stream. A transient failure
// (outage, 429/5xx) is retried with backoff before moving to the next provider;
// when all of them fail, the request is replayed on the fallback model (if set
// and the prompt fits its context window). Usage is recorded via the wrapping
// goroutine since usage arrives on the final chunk.
func (r *RoutingProvider) Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error) {
	names := r.order(req)
	if len(names) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	var lastErr error
	skipPrimary := req.UseFallback || (r.fallbackModel != "" && time.Now().UnixNano() < r.downUntil.Load())
	allTransient := true
	if !skipPrimary {
		for i, name := range names {
			p := r.providers[name]
			// If the request did not pin a model, use the provider's default model.
			pr := req
			if pr.Model == "" {
				pr.Model = p.Model()
			}
			var ch <-chan Chunk
			var err error
			for attempt := 0; ; attempt++ {
				ch, err = p.Chat(ctx, pr)
				if err == nil || ctx.Err() != nil || !IsTransient(err) || attempt >= len(RetryBackoff) {
					break
				}
				log.Printf("llm: provider %q (model %s) transient failure: %v — retry %d/%d in %s", name, pr.Model, err, attempt+1, len(RetryBackoff), RetryBackoff[attempt])
				if !SleepBackoff(ctx, RetryBackoff[attempt]) {
					break
				}
			}
			if err != nil {
				lastErr = err
				if !IsTransient(err) {
					allTransient = false
				}
				if i+1 < len(names) {
					log.Printf("llm: provider %q (model %s) failed: %v — falling back to %q", name, pr.Model, err, names[i+1])
				} else {
					log.Printf("llm: provider %q (model %s) failed: %v — no providers left", name, pr.Model, err)
				}
				continue
			}
			if i > 0 {
				log.Printf("llm: request served by FALLBACK provider %q (model %s) instead of %q", name, pr.Model, names[0])
			}
			return r.wrap(ctx, ch), nil
		}
		if ctx.Err() != nil {
			return nil, lastErr
		}
	}

	if r.fallbackModel != "" {
		primaryModel := req.Model
		if primaryModel == "" {
			primaryModel = r.providers[names[0]].Model()
		}
		switch {
		case primaryModel == r.fallbackModel:
			// already the fallback; nothing different to try
		case !req.UseFallback && !allTransient:
			// The request itself was rejected (bad request, auth, context too
			// long); a different model on the same account will not help.
		default:
			if max := r.ContextMax(r.fallbackModel); max > 0 && EstimateMessagesTokens(req.Messages, req.Tools) > max*9/10 {
				lastErr = fmt.Errorf("%v (fallback model %s not tried: the conversation is too long for its %d-token context)", lastErr, r.fallbackModel, max)
				break
			}
			if !skipPrimary {
				r.downUntil.Store(time.Now().Add(primaryDownFor).UnixNano())
			}
			fr := req
			fr.Model = r.fallbackModel
			p := r.providers[names[0]]
			ch, err := p.Chat(ctx, fr)
			if err == nil {
				log.Printf("llm: request served by FALLBACK MODEL %s (primary %s unavailable: %v)", r.fallbackModel, primaryModel, lastErr)
				return r.wrap(ctx, ch), nil
			}
			log.Printf("llm: fallback model %s failed too: %v", r.fallbackModel, err)
			if lastErr == nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("%v; fallback model %s: %w", lastErr, r.fallbackModel, err)
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no provider available")
	}
	return nil, fmt.Errorf("all providers failed: %w", lastErr)
}

// wrap forwards chunks and records usage from the terminal chunk.
func (r *RoutingProvider) wrap(ctx context.Context, in <-chan Chunk) <-chan Chunk {
	if r.recordUsage == nil {
		return in
	}
	out := make(chan Chunk, 32)
	go func() {
		defer close(out)
		for c := range in {
			if c.Done && c.Usage != nil {
				r.recordUsage(ctx, c.Provider, c.Model, *c.Usage)
			}
			out <- c
		}
	}()
	return out
}
