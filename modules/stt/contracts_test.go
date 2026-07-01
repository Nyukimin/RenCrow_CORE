package stt

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type fakeViewerInputObserver struct{}

func (fakeViewerInputObserver) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "stt.viewer_input", Status: core.HealthReady, Ready: true}
}

func (fakeViewerInputObserver) Snapshot(context.Context) (ViewerInputSnapshot, error) {
	return ViewerInputSnapshot{ChatInputEndpoint: "/stt/chat-input", TranscriptSource: "local_stt"}, nil
}

func TestViewerInputObserverContract(t *testing.T) {
	var observer ViewerInputObserver = fakeViewerInputObserver{}
	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.ChatInputEndpoint != "/stt/chat-input" || snapshot.TranscriptSource != "local_stt" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestCurrentTranscriptionPolicyDoesNotExecuteTranscription(t *testing.T) {
	policy := CurrentTranscriptionPolicy()
	if policy.EndpointExecutesTranscription {
		t.Fatalf("diagnostics policy must not execute transcription: %+v", policy)
	}
	if !policy.ViewerInputSeparated {
		t.Fatalf("viewer input must stay separated from transcription provider diagnostics: %+v", policy)
	}
	if !containsString(policy.RequiredRequestFields, "audio") {
		t.Fatalf("audio must remain the required transcription field: %+v", policy)
	}
}

func TestBuildDiagnosticsSnapshot(t *testing.T) {
	snapshot := BuildDiagnosticsSnapshot(context.Background(), fakeSTTProvider{}, testTime())
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || snapshot.Provider != "fake-stt" {
		t.Fatalf("unexpected diagnostics snapshot: %+v", snapshot)
	}
	if snapshot.Health.CheckedAt.IsZero() || snapshot.TranscriptionPolicy.EndpointExecutesTranscription {
		t.Fatalf("diagnostics metadata missing: %+v", snapshot)
	}
}

type fakeSTTProvider struct{}

func (fakeSTTProvider) Name() string {
	return "fake-stt"
}

func (fakeSTTProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "stt", Status: core.HealthReady, Ready: true}
}

func (fakeSTTProvider) Transcribe(context.Context, TranscriptionRequest) (TranscriptionResult, error) {
	return TranscriptionResult{}, nil
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
