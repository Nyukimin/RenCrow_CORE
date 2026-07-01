package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduleworker "github.com/Nyukimin/picoclaw_multiLLM/modules/worker"
)

type fakeModuleWorkerExecutor struct{}

func (fakeModuleWorkerExecutor) Health(context.Context) modulecore.HealthReport {
	return modulecore.HealthReport{Module: "worker", Status: modulecore.HealthReady, Ready: true}
}

func (fakeModuleWorkerExecutor) Execute(context.Context, moduleworker.Action) (moduleworker.Result, error) {
	return moduleworker.Result{}, nil
}

func TestHandleModuleWorkerDiagnostics(t *testing.T) {
	handler := handleModuleWorkerDiagnostics(fakeModuleWorkerExecutor{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/worker/diagnostics", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got moduleworker.DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Health.Module != "worker" || got.Health.CheckedAt.IsZero() {
		t.Fatalf("health was not included with checked_at: %+v", got.Health)
	}
	if len(got.SupportedTools) != 1 || got.SupportedTools[0].Name != moduleworker.ToolProposalPatch {
		t.Fatalf("supported tools missing: %+v", got.SupportedTools)
	}
}

func TestHandleModuleWorkerDiagnosticsUnavailable(t *testing.T) {
	handler := handleModuleWorkerDiagnostics(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/worker/diagnostics", nil)
	handler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
}
