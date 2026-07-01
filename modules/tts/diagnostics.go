package tts

import (
	"context"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type DiagnosticsSnapshot struct {
	UpdatedAt       string            `json:"updated_at"`
	Provider        string            `json:"provider,omitempty"`
	Health          core.HealthReport `json:"health"`
	SynthesisPolicy SynthesisPolicy   `json:"synthesis_policy"`
}

type SynthesisPolicy struct {
	EndpointExecutesSynthesis bool     `json:"endpoint_executes_synthesis"`
	RequiredRequestFields     []string `json:"required_request_fields"`
	OptionalRequestFields     []string `json:"optional_request_fields,omitempty"`
	PlaybackStateSeparated    bool     `json:"playback_state_separated"`
	Description               string   `json:"description"`
}

const DiagnosticsProviderUnavailableMessage = "tts provider unavailable"

func CurrentSynthesisPolicy() SynthesisPolicy {
	return SynthesisPolicy{
		EndpointExecutesSynthesis: false,
		RequiredRequestFields:     []string{"speech_text"},
		OptionalRequestFields:     []string{"session_id", "response_id", "utterance_id", "character_id", "voice_id", "display_text", "emotion"},
		PlaybackStateSeparated:    true,
		Description:               "Diagnostics endpoint does not synthesize audio; playback ACK and pending state are observed through tts.playback.",
	}
}

func BuildDiagnosticsSnapshot(ctx context.Context, provider Provider, updatedAt time.Time) DiagnosticsSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	health := provider.Health(ctx)
	if health.CheckedAt.IsZero() {
		health.CheckedAt = updatedAt
	}
	return DiagnosticsSnapshot{
		UpdatedAt:       updatedAt.UTC().Format(time.RFC3339),
		Provider:        provider.Name(),
		Health:          health,
		SynthesisPolicy: CurrentSynthesisPolicy(),
	}
}
