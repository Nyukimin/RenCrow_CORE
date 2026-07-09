package tts

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildProviderHealth(t *testing.T) {
	got := BuildProviderHealth(ProviderHealthSnapshot{Provider: "irodori", Ready: true})
	if got.Module != "tts" || got.Status != core.HealthLive || !got.Ready || got.Metadata["provider"] != "irodori" {
		t.Fatalf("unexpected ready health: %+v", got)
	}

	got = BuildProviderHealth(ProviderHealthSnapshot{})
	if got.Module != "tts" || got.Status != core.HealthDown || got.Ready || got.Detail != "tts provider is nil" {
		t.Fatalf("unexpected unavailable health: %+v", got)
	}
}
