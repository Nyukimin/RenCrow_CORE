package tts

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type fakePlaybackStateObserver struct{}

func (fakePlaybackStateObserver) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "tts.playback", Status: core.HealthReady, Ready: true}
}

func (fakePlaybackStateObserver) Snapshot(context.Context) (PlaybackStateSnapshot, error) {
	return PlaybackStateSnapshot{PendingSessionCount: 1, PublicRouteCount: 2}, nil
}

func TestPlaybackStateObserverContract(t *testing.T) {
	var observer PlaybackStateObserver = fakePlaybackStateObserver{}
	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.PendingSessionCount != 1 || snapshot.PublicRouteCount != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestCurrentSynthesisPolicyDoesNotExecuteSynthesis(t *testing.T) {
	policy := CurrentSynthesisPolicy()
	if policy.EndpointExecutesSynthesis {
		t.Fatalf("diagnostics policy must not execute synthesis: %+v", policy)
	}
	if !policy.PlaybackStateSeparated {
		t.Fatalf("playback state must stay separated from synthesis provider diagnostics: %+v", policy)
	}
	if !containsString(policy.RequiredRequestFields, "speech_text") {
		t.Fatalf("speech_text must remain the required synthesis field: %+v", policy)
	}
}

func TestBuildDiagnosticsSnapshot(t *testing.T) {
	snapshot := BuildDiagnosticsSnapshot(context.Background(), fakeTTSProvider{}, testTime())
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || snapshot.Provider != "fake-tts" {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Health.CheckedAt.IsZero() || snapshot.SynthesisPolicy.EndpointExecutesSynthesis {
		t.Fatalf("diagnostics metadata missing: %+v", snapshot)
	}
}

type fakeTTSProvider struct{}

func (fakeTTSProvider) Name() string {
	return "fake-tts"
}

func (fakeTTSProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "tts", Status: core.HealthLive, Ready: true}
}

func (fakeTTSProvider) Synthesize(context.Context, SynthesisRequest) (SynthesisResult, error) {
	return SynthesisResult{}, nil
}

func testTime() time.Time {
	return time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
