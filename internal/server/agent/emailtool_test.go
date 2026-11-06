package agent

import (
	"testing"
	"time"
)

func TestEmailLimiter(t *testing.T) {
	var l emailLimiter
	now := time.Now()
	for i := 0; i < emailPerHour; i++ {
		if !l.allow(1, now) {
			t.Fatalf("send %d refused", i+1)
		}
	}
	if l.allow(1, now) {
		t.Error("send over the hourly limit allowed")
	}
	if !l.allow(2, now) {
		t.Error("limit must be per user")
	}
	if !l.allow(1, now.Add(61*time.Minute)) {
		t.Error("window must slide after an hour")
	}
}
