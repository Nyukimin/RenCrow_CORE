package stt

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestCurrentTranscriptionPolicy(t *testing.T) {
	policy := CurrentTranscriptionPolicy()
	if policy.EndpointExecutesTranscription || !policy.ViewerInputSeparated {
		t.Fatalf("unexpected transcription policy: %+v", policy)
	}
	if !containsString(policy.RequiredRequestFields, "audio") {
		t.Fatalf("audio must be required: %+v", policy)
	}
	if DiagnosticsProviderUnavailableMessage != "stt provider unavailable" {
		t.Fatalf("unexpected unavailable message: %q", DiagnosticsProviderUnavailableMessage)
	}
}

func TestBuildSTTDiagnosticsSnapshot(t *testing.T) {
	snapshot := BuildDiagnosticsSnapshot(context.Background(), fakeDiagnosticsProvider{}, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || snapshot.Provider != "fake-stt" {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Health.CheckedAt.IsZero() || snapshot.TranscriptionPolicy.Description == "" {
		t.Fatalf("diagnostics metadata missing: %+v", snapshot)
	}
}

type fakeDiagnosticsProvider struct{}

func (fakeDiagnosticsProvider) Name() string {
	return "fake-stt"
}

func (fakeDiagnosticsProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "stt", Status: core.HealthReady, Ready: true}
}

func (fakeDiagnosticsProvider) Transcribe(context.Context, TranscriptionRequest) (TranscriptionResult, error) {
	return TranscriptionResult{}, nil
}
