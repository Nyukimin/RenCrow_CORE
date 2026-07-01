package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

func TestHandleModuleLLMDiagnostics(t *testing.T) {
	handler := handleModuleLLMDiagnostics(map[string]modulellm.Provider{
		"chat": fakeModuleLLMProvider{name: "chat-provider"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/modules/llm/diagnostics", nil)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var got modulellm.DiagnosticsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.UpdatedAt == "" || len(got.Roles) != 1 {
		t.Fatalf("unexpected diagnostics: %+v", got)
	}
	if got.GenerationPolicy.EndpointExecutesGeneration {
		t.Fatalf("diagnostics endpoint must not execute generation: %+v", got.GenerationPolicy)
	}
	if got.Roles[0].Health.Module != "llm:chat" {
		t.Fatalf("health module was not role-qualified: %+v", got.Roles[0])
	}
}
