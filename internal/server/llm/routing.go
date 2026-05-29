package llm

import (
	"context"
	"fmt"
)

// UsageRecorder is called after each completion with token usage. It is optional.
type UsageRecorder func(ctx context.Context, provider, model string, u Usage)

// RoutingProvider wraps an ordered set of providers and implements routing +
// fallback. It selects a provider by per-model rules (or the default) and, on
// failure, falls back to the next provider in the configured order.
type RoutingProvider struct {
	providers       map[string]*OpenAICompatible
	defaultProvider string
	fallbackOrder   []string
	modelRules      map[string]string
	recordUsage     UsageRecorder
}

// NewRoutingProvider builds a routing provider.
func NewRoutingProvider(providers map[string]*OpenAICompatible, defaultProvider string, fallbackOrder []string, modelRules map[string]string, rec UsageRecorder) *RoutingProvider {
	return &RoutingProvider{
		providers:       providers,
		defaultProvider: defaultProvider,
		fallbackOrder:   fallbackOrder,
		modelRules:      modelRules,
		recordUsage:     rec,
	}
}

// Name identifies the router.
func (r *RoutingProvider) Name() string { return "router" }

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

// Chat tries providers in order until one returns a stream. Usage is recorded
// via the wrapping goroutine since usage arrives on the final chunk.
func (r *RoutingProvider) Chat(ctx context.Context, req ChatRequest) (<-chan Chunk, error) {
	names := r.order(req)
	if len(names) == 0 {
		return nil, fmt.Errorf("no providers configured")
	}

	var lastErr error
	for _, name := range names {
		p := r.providers[name]
		// If the request did not pin a model, use the provider's default model.
		pr := req
		if pr.Model == "" {
			pr.Model = p.Model()
		}
		ch, err := p.Chat(ctx, pr)
		if err != nil {
			lastErr = err
			continue
		}
		return r.wrap(ctx, ch), nil
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
