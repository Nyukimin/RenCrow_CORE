package config

import "testing"

func TestWorldProjectionLifePolicyDefaultsToEnabled(t *testing.T) {
	settings := WorldProjectionConfig{}
	if !settings.IsLifePolicyEnabled() {
		t.Fatal("life policy must default to enabled for an enabled projection")
	}
	disabled := false
	settings.LifePolicyEnabled = &disabled
	if settings.IsLifePolicyEnabled() {
		t.Fatal("explicit false was ignored")
	}
	enabled := true
	settings.LifePolicyEnabled = &enabled
	if !settings.IsLifePolicyEnabled() {
		t.Fatal("explicit true was ignored")
	}
}
