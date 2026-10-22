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

// D-06: an authorized source epoch switch stays closed by default. Enabling it
// is a deployment decision and requires its own credential environment name.
func TestWorldProjectionReconcileDefaultsToDisabled(t *testing.T) {
	settings := WorldProjectionConfig{}
	if settings.IsReconcileEnabled() {
		t.Fatal("source epoch switch must default to disabled")
	}
	settings.ReconcileEnabled = true
	if !settings.IsReconcileEnabled() {
		t.Fatal("explicit true was ignored")
	}
}
