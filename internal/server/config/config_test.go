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
