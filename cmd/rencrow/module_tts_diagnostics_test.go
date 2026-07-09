package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type fakeModuleTTSProvider struct{}

func (fakeModuleTTSProvider) Name() string {
	return "fake-tts"
}

func (fakeModuleTTSProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "tts", Status: core.HealthLive, Ready: true}
}

func (fakeModuleTTSProvider) Synthesize(context.Context, moduletts.SynthesisRequest) (moduletts.SynthesisResult, error) {
	return moduletts.SynthesisResult{}, nil
}

func TestHandleModuleTTSDiagnostics(t *testing.T) {
	handler := handleModuleTTSDiagnostics(fakeModuleTTSProvider{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/tts/diagnostics", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got moduletts.DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Provider != "fake-tts" || got.Health.CheckedAt.IsZero() {
		t.Fatalf("provider health missing: %+v", got)
	}
	if got.SynthesisPolicy.EndpointExecutesSynthesis || !got.SynthesisPolicy.PlaybackStateSeparated {
		t.Fatalf("unexpected synthesis policy: %+v", got.SynthesisPolicy)
	}
}

func TestHandleModuleTTSDiagnosticsUnavailable(t *testing.T) {
	handler := handleModuleTTSDiagnostics(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/tts/diagnostics", nil)
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
