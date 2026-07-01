package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	modulestt "github.com/Nyukimin/picoclaw_multiLLM/modules/stt"
)

type fakeModuleSTTProvider struct{}

func (fakeModuleSTTProvider) Name() string {
	return "fake-stt"
}

func (fakeModuleSTTProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "stt", Status: core.HealthReady, Ready: true}
}

func (fakeModuleSTTProvider) Transcribe(context.Context, modulestt.TranscriptionRequest) (modulestt.TranscriptionResult, error) {
	return modulestt.TranscriptionResult{}, nil
}

func TestHandleModuleSTTDiagnostics(t *testing.T) {
	handler := handleModuleSTTDiagnostics(fakeModuleSTTProvider{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/stt/diagnostics", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got modulestt.DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Provider != "fake-stt" || got.Health.CheckedAt.IsZero() {
		t.Fatalf("provider health missing: %+v", got)
	}
	if got.TranscriptionPolicy.EndpointExecutesTranscription || !got.TranscriptionPolicy.ViewerInputSeparated {
		t.Fatalf("unexpected transcription policy: %+v", got.TranscriptionPolicy)
	}
}

func TestHandleModuleSTTDiagnosticsUnavailable(t *testing.T) {
	handler := handleModuleSTTDiagnostics(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/stt/diagnostics", nil)
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
