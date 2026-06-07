package cronspec

import (
	"testing"
	"time"
)

func TestNextStandard(t *testing.T) {
	// Every minute: next is within (0, 60] seconds.
	base := time.Date(2026, 6, 7, 10, 30, 15, 0, time.UTC)
	next, err := Next("* * * * *", base)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !next.Equal(time.Date(2026, 6, 7, 10, 31, 0, 0, time.UTC)) {
		t.Fatalf("next = %v", next)
	}
}

func TestNextHourly(t *testing.T) {
	base := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	next, err := Next("0 * * * *", base)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if next.Hour() != 11 || next.Minute() != 0 {
		t.Fatalf("next = %v", next)
	}
}

func TestEvery(t *testing.T) {
	base := time.Date(2026, 6, 7, 10, 30, 0, 0, time.UTC)
	next, err := Next("@every 5m", base)
	if err != nil {
		t.Fatalf("@every: %v", err)
	}
	if d := next.Sub(base); d != 5*time.Minute {
		t.Fatalf("delta = %v, want 5m", d)
	}
}

func TestDescriptors(t *testing.T) {
	if err := Valid("@daily"); err != nil {
		t.Errorf("@daily should be valid: %v", err)
	}
	if err := Valid("@hourly"); err != nil {
		t.Errorf("@hourly should be valid: %v", err)
	}
}

func TestInvalid(t *testing.T) {
	for _, bad := range []string{"", "not a spec", "* * *", "60 * * * *"} {
		if err := Valid(bad); err == nil {
			t.Errorf("Valid(%q) = nil, want error", bad)
		}
	}
}
