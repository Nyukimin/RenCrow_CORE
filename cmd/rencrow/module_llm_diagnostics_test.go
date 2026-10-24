package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func TestHandleModuleLLMDiagnostics(t *testing.T) {
	durationNS := int64(41)
	handler := handleModuleLLMDiagnostics(map[string]modulellm.Provider{
		"chat": llmDiagnosticsHandlerTestProvider{
			diagnostics: &modulellm.GatewayRequestWriteDiagnosticsSnapshot{
				Capacity: 1,
				Requests: []modulellm.GatewayRequestWriteSample{{
					RequestID:           "request-1",
					DoStartedAtUTC:      "2026-05-30T01:02:03Z",
					WroteRequestAtUTC:   "2026-05-30T01:02:03.000000041Z",
					DoToWriteDurationNS: &durationNS,
				}},
			},
		},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/modules/llm/diagnostics", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("diagnostics response cache-control=%q want no-store", got)
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
	if got.Roles[0].GatewayRequestWrite == nil || len(got.Roles[0].GatewayRequestWrite.Requests) != 1 || got.Roles[0].GatewayRequestWrite.Requests[0].RequestID != "request-1" {
		t.Fatalf("Gateway write diagnostics were not exported: %+v", got.Roles[0].GatewayRequestWrite)
	}
}

func TestHandleModuleLLMDiagnosticsHidesGatewayWriteFromNonLoopback(t *testing.T) {
	durationNS := int64(41)
	handler := handleModuleLLMDiagnostics(map[string]modulellm.Provider{
		"chat": llmDiagnosticsHandlerTestProvider{
			diagnostics: &modulellm.GatewayRequestWriteDiagnosticsSnapshot{
				Capacity: 1,
				Requests: []modulellm.GatewayRequestWriteSample{{
					RequestID:           "request-private",
					DoStartedAtUTC:      "2026-05-30T01:02:03Z",
					WroteRequestAtUTC:   "2026-05-30T01:02:03.000000041Z",
					DoToWriteDurationNS: &durationNS,
				}},
			},
		},
	})

	for _, test := range []struct {
		name       string
		host       string
		remoteAddr string
	}{
		{name: "direct LAN", host: "127.0.0.1", remoteAddr: "192.0.2.10:1234"},
		{name: "loopback peer with external Host", host: "192.0.2.10", remoteAddr: "127.0.0.1:1234"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/viewer/modules/llm/diagnostics", nil)
			req.RemoteAddr = test.remoteAddr
			handler(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
			}
			var got modulellm.DiagnosticsSnapshot
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if len(got.Roles) != 1 || got.Roles[0].GatewayRequestWrite != nil {
				t.Fatalf("non-loopback request exposed Gateway write diagnostics: %+v", got.Roles)
			}
			if got.Roles[0].Health.Module != "llm:chat" || !got.Roles[0].Health.Ready {
				t.Fatalf("existing provider health was changed by diagnostic field gating: %+v", got.Roles[0].Health)
			}
		})
	}
}

type llmDiagnosticsHandlerTestProvider struct {
	diagnostics *modulellm.GatewayRequestWriteDiagnosticsSnapshot
}

func (p llmDiagnosticsHandlerTestProvider) Name() string {
	return "chat-provider"
}

func (p llmDiagnosticsHandlerTestProvider) Health(context.Context) modulecore.HealthReport {
	return modulecore.HealthReport{Status: modulecore.HealthLive, Ready: true}
}

func (p llmDiagnosticsHandlerTestProvider) Generate(context.Context, modulellm.GenerateRequest) (modulellm.GenerateResponse, error) {
	return modulellm.GenerateResponse{Content: "ok"}, nil
}

func (p llmDiagnosticsHandlerTestProvider) GatewayRequestWriteDiagnostics() *modulellm.GatewayRequestWriteDiagnosticsSnapshot {
	return p.diagnostics
}
