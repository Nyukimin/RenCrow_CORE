package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

type fakeModuleLLMProvider struct {
	name string
}

func (p fakeModuleLLMProvider) Name() string {
	return p.name
}

func (p fakeModuleLLMProvider) Health(context.Context) modulecore.HealthReport {
	return modulecore.HealthReport{
		Module: "llm",
		Status: modulecore.HealthLive,
		Ready:  true,
		Metadata: map[string]any{
			"provider": p.name,
		},
	}
}

func (p fakeModuleLLMProvider) Generate(context.Context, modulellm.GenerateRequest) (modulellm.GenerateResponse, error) {
	return modulellm.GenerateResponse{Content: "ok"}, nil
}

type fakeModuleHealthProvider struct {
	module string
	status modulecore.HealthStatus
}

func (p fakeModuleHealthProvider) Health(context.Context) modulecore.HealthReport {
	return modulecore.HealthReport{
		Module: p.module,
		Status: p.status,
		Ready:  p.status == modulecore.HealthReady || p.status == modulecore.HealthLive,
	}
}

func TestBuildModuleHealthSnapshotOrdersLLMRolesAndIncludesAudioModules(t *testing.T) {
	snapshot := buildModuleHealthSnapshot(context.Background(), map[string]modulellm.Provider{
		"worker": fakeModuleLLMProvider{name: "worker-provider"},
		"chat":   fakeModuleLLMProvider{name: "chat-provider"},
	}, fakeModuleHealthProvider{module: "chat", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "tts", status: modulecore.HealthLive}, fakeModuleHealthProvider{module: "tts.playback", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "stt", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "stt.viewer_input", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "worker", status: modulecore.HealthReady}, testModuleHealthTime())

	reports := snapshot.Modules
	if len(reports) != 8 {
		t.Fatalf("expected 8 reports, got %+v", reports)
	}
	if reports[0].Module != "llm:chat" || reports[1].Module != "llm:worker" {
		t.Fatalf("llm reports were not role-sorted: %+v", reports)
	}
	if reports[2].Module != "chat" || reports[3].Module != "worker" || reports[4].Module != "tts" || reports[5].Module != "tts.playback" || reports[6].Module != "stt" || reports[7].Module != "stt.viewer_input" {
		t.Fatalf("module reports missing: %+v", reports)
	}
	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" {
		t.Fatalf("unexpected snapshot updated_at: %+v", snapshot)
	}
}

func TestHandleModuleHealthReturnsJSON(t *testing.T) {
	handler := handleModuleHealth(map[string]modulellm.Provider{
		"chat": fakeModuleLLMProvider{name: "chat-provider"},
	}, fakeModuleHealthProvider{module: "chat", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "tts", status: modulecore.HealthLive}, fakeModuleHealthProvider{module: "tts.playback", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "stt", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "stt.viewer_input", status: modulecore.HealthReady}, fakeModuleHealthProvider{module: "worker", status: modulecore.HealthReady})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/health", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var snapshot modulecore.HealthSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(snapshot.Modules) != 7 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Status != modulecore.HealthLive || !snapshot.Ready {
		t.Fatalf("unexpected aggregate status: %+v", snapshot)
	}
}

func testModuleHealthTime() time.Time {
	return time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC)
}
