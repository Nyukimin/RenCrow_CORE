package tts

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

func TestCurrentSynthesisPolicy(t *testing.T) {
	policy := CurrentSynthesisPolicy()
	if policy.EndpointExecutesSynthesis || !policy.PlaybackStateSeparated {
		t.Fatalf("unexpected synthesis policy: %+v", policy)
	}
	if !containsString(policy.RequiredRequestFields, "speech_text") {
		t.Fatalf("speech_text must be required: %+v", policy)
	}
	if DiagnosticsProviderUnavailableMessage != "tts provider unavailable" {
		t.Fatalf("unexpected unavailable message: %q", DiagnosticsProviderUnavailableMessage)
	}
}

func TestBuildTTSDiagnosticsSnapshot(t *testing.T) {
	snapshot := BuildDiagnosticsSnapshot(context.Background(), fakeDiagnosticsProvider{}, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || snapshot.Provider != "fake-tts" {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Health.CheckedAt.IsZero() || snapshot.SynthesisPolicy.Description == "" {
		t.Fatalf("diagnostics metadata missing: %+v", snapshot)
	}
}

type fakeDiagnosticsProvider struct{}

func (fakeDiagnosticsProvider) Name() string {
	return "fake-tts"
}

func (fakeDiagnosticsProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "tts", Status: core.HealthReady, Ready: true}
}

func (fakeDiagnosticsProvider) Synthesize(context.Context, SynthesisRequest) (SynthesisResult, error) {
	return SynthesisResult{}, nil
}
