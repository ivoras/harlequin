package agent

import (
	"testing"
	"time"
)

func TestBuildTurnTiming(t *testing.T) {
	// 1000 prompt tokens in 500ms -> 2000 tok/s PP; 200 completion in 4s -> 50 tok/s TG.
	got := buildTurnTiming(1000, 200, 500*time.Millisecond, 4*time.Second, 5*time.Second)
	if got == nil {
		t.Fatal("nil timing")
	}
	if got.PPRate < 1999 || got.PPRate > 2001 {
		t.Errorf("PPRate = %v, want ~2000", got.PPRate)
	}
	if got.TGRate < 49.9 || got.TGRate > 50.1 {
		t.Errorf("TGRate = %v, want ~50", got.TGRate)
	}
	if got.TotalMS != 5000 || got.PrefillMS != 500 || got.DecodeMS != 4000 {
		t.Errorf("durations: %+v", got)
	}
}

func TestBuildTurnTimingNoData(t *testing.T) {
	if got := buildTurnTiming(0, 0, 0, 0, 0); got != nil {
		t.Errorf("expected nil for zero total, got %+v", got)
	}
	// Total but no tokens: rates stay zero, struct still returned.
	got := buildTurnTiming(0, 0, 0, 0, time.Second)
	if got == nil || got.PPRate != 0 || got.TGRate != 0 {
		t.Errorf("got %+v", got)
	}
}

func TestTimingFromServer(t *testing.T) {
	// Aggregated over two calls: 60 prompt tokens in 100ms total -> 600 tok/s PP;
	// 300 predicted in 6000ms -> 50 tok/s TG. Wall clock 7s.
	got := timingFromServer(60, 300, 100, 6000, 7*time.Second)
	if got == nil {
		t.Fatal("nil")
	}
	if got.PPRate < 599 || got.PPRate > 601 {
		t.Errorf("PPRate = %v, want ~600", got.PPRate)
	}
	if got.TGRate < 49.9 || got.TGRate > 50.1 {
		t.Errorf("TGRate = %v, want ~50", got.TGRate)
	}
	if got.PrefillMS != 100 || got.DecodeMS != 6000 || got.TotalMS != 7000 {
		t.Errorf("durations: %+v", got)
	}
}
