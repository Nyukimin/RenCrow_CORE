package stt

import (
	"context"
	"testing"
	"time"
)

func TestBuildViewerInputReport(t *testing.T) {
	snapshot := ViewerInputSnapshot{ChatInputEndpoint: "/stt/chat-input", TranscriptSource: "local_stt"}
	report := BuildViewerInputReport(context.Background(), fakeViewerInputObserver{}, snapshot, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))

	if report.UpdatedAt != "2026-05-30T01:02:03Z" {
		t.Fatalf("unexpected updated_at: %+v", report)
	}
	if report.Health.Module != "stt.viewer_input" || report.Health.CheckedAt.IsZero() {
		t.Fatalf("health was not normalized: %+v", report.Health)
	}
	if report.Snapshot.ChatInputEndpoint != "/stt/chat-input" || report.Snapshot.TranscriptSource != "local_stt" {
		t.Fatalf("snapshot was not preserved: %+v", report.Snapshot)
	}
}

func TestBuildViewerInputSnapshotAppliesRuntimeDefaults(t *testing.T) {
	got := BuildViewerInputSnapshot(ViewerInputRuntimeConfig{
		BaseURL:            "http://127.0.0.1:8766/",
		StreamURL:          " ws://127.0.0.1:8766/stt ",
		ProviderURL:        " http://127.0.0.1:8766/stt/file ",
		GatewayURL:         " ws://127.0.0.1:8766/stt ",
		WebSocketAvailable: true,
	})

	if got.BaseURL != "http://127.0.0.1:8766" || got.StreamURL != "ws://127.0.0.1:8766/stt" {
		t.Fatalf("urls were not normalized: %+v", got)
	}
	if got.ChatInputEndpoint != DefaultViewerChatInputEndpoint || got.TranscriptInjectPath != DefaultViewerChatInputEndpoint {
		t.Fatalf("chat input defaults were not applied: %+v", got)
	}
	if got.ClientLogPath == "" || got.LatestWAVPath == "" || got.AutoTestScriptPath == "" || got.AutoTestOutputPath == "" {
		t.Fatalf("debug artifact defaults were not applied: %+v", got)
	}
	if !got.ProviderConfigured || !got.GatewayConfigured || !got.WebSocketConfigured {
		t.Fatalf("configured flags were not set: %+v", got)
	}
	if got.TranscriptSource != DefaultViewerTranscriptSource || got.TranscriptInputType != DefaultViewerTranscriptType {
		t.Fatalf("transcript defaults were not applied: %+v", got)
	}
}

func TestBuildViewerInputHealthReport(t *testing.T) {
	ready := BuildViewerInputHealthReport(ViewerInputSnapshot{
		ChatInputEndpoint:   "/stt/chat-input",
		StreamURL:           "ws://127.0.0.1:8766/stt",
		ProviderConfigured:  true,
		WebSocketConfigured: true,
		GatewayConfigured:   true,
	})
	if ready.Module != "stt.viewer_input" || ready.Status != "ready" || !ready.Ready {
		t.Fatalf("ready health = %+v", ready)
	}
	if ready.Metadata["gateway_configured"] != true || ready.Metadata["chat_input_endpoint"] != "/stt/chat-input" {
		t.Fatalf("ready metadata = %+v", ready.Metadata)
	}

	blocked := BuildViewerInputHealthReport(ViewerInputSnapshot{ChatInputEndpoint: "/stt/chat-input"})
	if blocked.Status != "blocked" || blocked.Ready {
		t.Fatalf("blocked health = %+v", blocked)
	}
}

func TestViewerInputEndpointMessages(t *testing.T) {
	if ViewerInputObserverUnavailableMessage != "stt viewer input observer unavailable" {
		t.Fatalf("unexpected unavailable message: %q", ViewerInputObserverUnavailableMessage)
	}
	if ViewerInputSnapshotFailedPrefix != "stt viewer input snapshot failed: " {
		t.Fatalf("unexpected snapshot failed prefix: %q", ViewerInputSnapshotFailedPrefix)
	}
}

func TestBuildViewerInputArchivePath(t *testing.T) {
	got := BuildViewerInputArchivePath("tmp/stt_inputs", time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))
	want := "tmp/stt_inputs/client_stt_input_20260530_010203.wav"
	if got != want {
		t.Fatalf("archive path = %q, want %q", got, want)
	}
}

func TestBuildViewerInputArchivePathUsesDefaultDir(t *testing.T) {
	got := BuildViewerInputArchivePath(" ", time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))
	want := "tmp/stt_inputs/client_stt_input_20260530_010203.wav"
	if got != want {
		t.Fatalf("archive path = %q, want %q", got, want)
	}
}

func TestBuildViewerInputRawArchivePath(t *testing.T) {
	got := BuildViewerInputRawArchivePath("tmp/stt_inputs", time.Date(2026, 6, 9, 13, 35, 8, 0, time.UTC))
	want := "tmp/stt_inputs/client_stt_input_20260609_133508_raw.wav"
	if got != want {
		t.Fatalf("raw archive path = %q, want %q", got, want)
	}
}
