package config

import "testing"

func TestSessionsConfigEnabledValue(t *testing.T) {
	t.Parallel()
	if !(SessionsConfig{}).EnabledValue() {
		t.Fatal("default should be enabled")
	}
	off := false
	disabled := SessionsConfig{Enabled: &off}
	if disabled.EnabledValue() {
		t.Fatal("explicit false should disable")
	}
	on := true
	enabled := SessionsConfig{Enabled: &on}
	if !enabled.EnabledValue() {
		t.Fatal("explicit true should enable")
	}
}

func TestDataRetentionDaysValue(t *testing.T) {
	t.Parallel()
	if got := (Config{}).DataRetentionDaysValue(); got != 7 {
		t.Fatalf("default retention: got %d want 7", got)
	}
	forever := 0
	if got := (Config{DataRetentionDays: &forever}).DataRetentionDaysValue(); got != 0 {
		t.Fatalf("explicit 0: got %d", got)
	}
	custom := 14
	if got := (Config{DataRetentionDays: &custom}).DataRetentionDaysValue(); got != 14 {
		t.Fatalf("explicit 14: got %d", got)
	}
}
