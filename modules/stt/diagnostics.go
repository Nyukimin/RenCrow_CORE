package stt

import (
	"context"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type DiagnosticsSnapshot struct {
	UpdatedAt           string              `json:"updated_at"`
	Provider            string              `json:"provider,omitempty"`
	Health              core.HealthReport   `json:"health"`
	TranscriptionPolicy TranscriptionPolicy `json:"transcription_policy"`
}

type TranscriptionPolicy struct {
	EndpointExecutesTranscription bool     `json:"endpoint_executes_transcription"`
	RequiredRequestFields         []string `json:"required_request_fields"`
	OptionalRequestFields         []string `json:"optional_request_fields,omitempty"`
	ViewerInputSeparated          bool     `json:"viewer_input_separated"`
	Description                   string   `json:"description"`
}

const DiagnosticsProviderUnavailableMessage = "stt provider unavailable"

func CurrentTranscriptionPolicy() TranscriptionPolicy {
	return TranscriptionPolicy{
		EndpointExecutesTranscription: false,
		RequiredRequestFields:         []string{"audio"},
		OptionalRequestFields:         []string{"session_id", "request_id", "format", "language", "prompt"},
		ViewerInputSeparated:          true,
		Description:                   "Diagnostics endpoint does not transcribe audio; Viewer microphone and transcript injection state are observed through stt.viewer_input.",
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
		UpdatedAt:           updatedAt.UTC().Format(time.RFC3339),
		Provider:            provider.Name(),
		Health:              health,
		TranscriptionPolicy: CurrentTranscriptionPolicy(),
	}
}
