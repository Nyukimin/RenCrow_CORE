package tts

import "github.com/Nyukimin/RenCrow_CORE/modules/core"

type ProviderHealthSnapshot struct {
	Provider string
	Ready    bool
}

func BuildProviderHealth(snapshot ProviderHealthSnapshot) core.HealthReport {
	if !snapshot.Ready {
		return core.HealthReport{Module: "tts", Status: core.HealthDown, Detail: "tts provider is nil"}
	}
	return core.HealthReport{
		Module:   "tts",
		Status:   core.HealthLive,
		Ready:    true,
		Metadata: map[string]any{"provider": snapshot.Provider},
	}
}
