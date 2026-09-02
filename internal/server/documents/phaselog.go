package documents

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// This file carries one ingestion's phase timings across the packages that
// perform them: the api package owns extraction and the description call, the
// documents package owns chunking, embedding and the database write. A single
// PhaseLog travels between them on the request context, so one upload produces
// one coherent timeline instead of two partial ones.
//
// Every line carries an absolute RFC3339 timestamp of its own. The server's
// `-log-timestamps` flag is off by default (journald and docker add their own),
// so a duration-only line would be impossible to place in time when someone
// pastes a log excerpt — the timestamps have to be in the message itself.

// PhaseLog accumulates the phases of one ingestion and logs each as it ends.
// A nil *PhaseLog is a working no-op, so callers never need a nil check.
type PhaseLog struct {
	label string

	mu     sync.Mutex
	begin  time.Time
	last   time.Time
	phases []phaseRec
	done   bool
}

type phaseRec struct {
	name string
	dur  time.Duration
}

// NewPhaseLog starts timing an ingestion. label identifies the document in the
// log — the uploaded filename, or the title for a raw-text ingest.
func NewPhaseLog(label string) *PhaseLog {
	now := time.Now()
	if strings.TrimSpace(label) == "" {
		label = "(untitled)"
	}
	return &PhaseLog{label: label, begin: now, last: now}
}

// Relabel renames the ingestion once its real identity is known. The label has
// to be chosen before the upload is parsed, so a multipart route starts with a
// placeholder and calls this with the filename once it has one. Ignores empty
// names, so a raw-text ingest with no title keeps the placeholder.
func (p *PhaseLog) Relabel(label string) {
	if p == nil || strings.TrimSpace(label) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.label = label
}

// phaseLogKey is the context key under which a PhaseLog travels.
type phaseLogKey struct{}

// WithPhaseLog returns a context carrying pl, so the ingest phases performed in
// other packages join the same timeline.
func WithPhaseLog(ctx context.Context, pl *PhaseLog) context.Context {
	if pl == nil {
		return ctx
	}
	return context.WithValue(ctx, phaseLogKey{}, pl)
}

// PhaseLogFrom returns the PhaseLog on ctx, or nil (which is a usable no-op).
func PhaseLogFrom(ctx context.Context) *PhaseLog {
	pl, _ := ctx.Value(phaseLogKey{}).(*PhaseLog)
	return pl
}

// Phase closes the phase that just ended, logging when it started, how long it
// took, and how long the ingestion has run in total. detail is free-form context
// for that phase ("1443 sentences", "40 pages") and may be empty.
func (p *PhaseLog) Phase(name string, detail ...any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	dur := now.Sub(p.last)
	start := p.last
	p.last = now
	p.phases = append(p.phases, phaseRec{name: name, dur: dur})

	extra := ""
	if len(detail) > 0 {
		if s := strings.TrimSpace(fmt.Sprint(detail...)); s != "" {
			extra = " " + s
		}
	}
	log.Printf("documents: ingest %q phase=%s start=%s dur=%s elapsed=%s%s",
		p.label, name,
		start.Format(time.RFC3339Nano),
		dur.Round(time.Millisecond),
		now.Sub(p.begin).Round(time.Millisecond),
		extra)
}

// Done emits the summary line: the total, and the breakdown in one place so a
// slow ingestion can be attributed without reassembling the individual lines.
// Calling it twice logs once — the sync and async upload paths both defer it.
func (p *PhaseLog) Done() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.done = true

	total := time.Since(p.begin)
	parts := make([]string, 0, len(p.phases))
	for _, ph := range p.phases {
		pct := 0
		if total > 0 {
			pct = int(ph.dur * 100 / total)
		}
		parts = append(parts, fmt.Sprintf("%s %s (%d%%)", ph.name, ph.dur.Round(time.Millisecond), pct))
	}
	breakdown := "no phases recorded"
	if len(parts) > 0 {
		breakdown = strings.Join(parts, ", ")
	}
	log.Printf("documents: ingest %q complete start=%s end=%s total=%s: %s",
		p.label,
		p.begin.Format(time.RFC3339Nano),
		time.Now().Format(time.RFC3339Nano),
		total.Round(time.Millisecond),
		breakdown)
}
