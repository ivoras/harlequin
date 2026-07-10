package llm

import (
	"testing"
	"time"
)

func TestPPTrackerRollingWindow(t *testing.T) {
	tr := newPPTracker()
	if got := tr.avg(); got != 0 {
		t.Fatalf("empty avg = %v, want 0", got)
	}
	// Add more than the window; only the last ppWindow samples should count.
	for i := 1; i <= ppWindow+5; i++ {
		tr.add(float64(i))
	}
	// Last ppWindow values are 6..15; their mean is 10.5.
	if got := tr.avg(); got != 10.5 {
		t.Fatalf("rolling avg = %v, want 10.5", got)
	}
	// Non-positive rates are ignored.
	tr.add(0)
	tr.add(-3)
	if got := tr.avg(); got != 10.5 {
		t.Fatalf("avg after bad samples = %v, want 10.5", got)
	}
}

func TestRequestTimeout(t *testing.T) {
	p := NewOpenAICompatible("test", "http://x", "", "m")

	// No samples yet -> fixed fallback.
	if got := p.requestTimeout(1000); got != defaultRequestTimeout {
		t.Fatalf("fallback timeout = %v, want %v", got, defaultRequestTimeout)
	}

	cases := []struct {
		name string
		rate float64 // avg PP tok/s
		est  int     // estimated prompt tokens
		want time.Duration
	}{
		// predicted = est/rate = 50s; 10x = 500s, within bounds.
		{"midrange", 100, 5000, 500 * time.Second},
		// predicted = 0.1s; 10x = 1s -> clamped up to the floor.
		{"below floor", 1000, 100, minRequestTimeout},
		// predicted = 10000s; 10x = 100000s -> clamped down to the ceiling.
		{"above ceiling", 10, 100000, maxRequestTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newPPTracker()
			tr.add(tc.rate)
			p.ppAvg = tr
			if got := p.requestTimeout(tc.est); got != tc.want {
				t.Fatalf("requestTimeout(%d) at %v tok/s = %v, want %v", tc.est, tc.rate, got, tc.want)
			}
		})
	}
}

func TestIdleWatchdog(t *testing.T) {
	waitFired := func(w *idleWatchdog) bool {
		select {
		case <-w.fired:
			return true
		case <-time.After(time.Second):
			return false
		}
	}

	t.Run("fires when idle", func(t *testing.T) {
		cancelled := make(chan struct{})
		w := newIdleWatchdog(10*time.Millisecond, func() { close(cancelled) })
		if !waitFired(w) {
			t.Fatal("watchdog did not fire")
		}
		<-cancelled // cancel ran before fired closed
		if !w.expired() {
			t.Fatal("expired() = false after firing")
		}
	})

	t.Run("touch keeps it alive", func(t *testing.T) {
		w := newIdleWatchdog(50*time.Millisecond, func() {})
		for range 5 {
			time.Sleep(20 * time.Millisecond)
			w.touch()
			if w.expired() {
				t.Fatal("fired despite regular touches")
			}
		}
		w.stop()
	})

	t.Run("stop disarms", func(t *testing.T) {
		w := newIdleWatchdog(10*time.Millisecond, func() { t.Error("cancel ran after stop") })
		w.stop()
		time.Sleep(30 * time.Millisecond)
		if w.expired() {
			t.Fatal("fired after stop")
		}
	})
}

func TestRecordPPPrefersServerTimings(t *testing.T) {
	p := NewOpenAICompatible("test", "http://x", "", "m")
	// Server timings: 1000 prompt tokens in 500ms -> 2000 tok/s.
	p.recordPP(nil, &Timings{PromptN: 1000, PromptMS: 500}, time.Now(), time.Time{})
	if got := p.ppAvg.avg(); got != 2000 {
		t.Fatalf("avg from server timings = %v, want 2000", got)
	}

	// Wall-clock fallback when no usable server timings: 800 processed tokens over
	// a 2s prefill -> 400 tok/s.
	p2 := NewOpenAICompatible("test", "http://x", "", "m")
	start := time.Now()
	p2.recordPP(&Usage{PromptTokens: 800}, nil, start, start.Add(2*time.Second))
	if got := p2.ppAvg.avg(); got != 400 {
		t.Fatalf("avg from wall-clock = %v, want 400", got)
	}

	// Tiny prefills are ignored as noise.
	p3 := NewOpenAICompatible("test", "http://x", "", "m")
	p3.recordPP(nil, &Timings{PromptN: minPPSampleTokens - 1, PromptMS: 1}, time.Now(), time.Time{})
	if got := p3.ppAvg.avg(); got != 0 {
		t.Fatalf("avg from tiny sample = %v, want 0 (ignored)", got)
	}
}
