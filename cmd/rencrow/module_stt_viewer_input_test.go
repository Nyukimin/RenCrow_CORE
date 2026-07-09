package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

type fakeSTTViewerInputObserver struct{}

func (fakeSTTViewerInputObserver) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "stt.viewer_input", Status: core.HealthReady, Ready: true}
}

func (fakeSTTViewerInputObserver) Snapshot(context.Context) (modulestt.ViewerInputSnapshot, error) {
	return modulestt.ViewerInputSnapshot{
		ChatInputEndpoint:   "/stt/chat-input",
		TranscriptSource:    "local_stt",
		TranscriptInputType: "voice",
	}, nil
}

func TestNewSTTViewerInputObserver(t *testing.T) {
	observer := newSTTViewerInputObserver(sttRuntime{
		ProviderURL: "http://127.0.0.1:8766/stt/file",
		GatewayURL:  "ws://127.0.0.1:8766/stt",
		DebugOptions: viewer.DebugSystemOptions{
			STTBaseURL:   "http://127.0.0.1:8766/",
			STTStreamURL: "ws://127.0.0.1:8766/stt",
		},
	})
	snapshot, err := observer.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot returned error: %v", err)
	}
	if snapshot.BaseURL != "http://127.0.0.1:8766" || snapshot.StreamURL != "ws://127.0.0.1:8766/stt" {
		t.Fatalf("urls were not normalized: %+v", snapshot)
	}
	if !snapshot.ProviderConfigured || !snapshot.GatewayConfigured || !snapshot.WebSocketConfigured {
		t.Fatalf("configured flags were not set: %+v", snapshot)
	}
	if snapshot.TranscriptInjectPath != "/stt/chat-input" || snapshot.TranscriptSource != "local_stt" {
		t.Fatalf("transcript injection contract was not set: %+v", snapshot)
	}
}

func TestHandleModuleSTTViewerInput(t *testing.T) {
	handler := handleModuleSTTViewerInput(fakeSTTViewerInputObserver{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/stt/viewer-input", nil)
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got modulestt.ViewerInputReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Health.Module != "stt.viewer_input" || got.Snapshot.ChatInputEndpoint != "/stt/chat-input" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Health.CheckedAt.IsZero() {
		t.Fatalf("health checked_at was not set: %+v", got.Health)
	}
}
