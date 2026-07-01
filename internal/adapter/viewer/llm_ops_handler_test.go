package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleLLMOpsStatus_ProxiesBearer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer testtok" {
			t.Fatalf("unexpected auth: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsStatus(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "testtok"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/status", nil)
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["ok"] != "1" {
		t.Fatalf("body: %+v", m)
	}
}

func TestLLMOpsProxyTimeoutCoversModelStartupWait(t *testing.T) {
	if llmOpsProxyTimeout < 10*time.Minute {
		t.Fatalf("llm ops proxy timeout should cover 600s startup wait, got %s", llmOpsProxyTimeout)
	}
	if llmOpsProxyTimeoutFor(http.MethodPost, "/v1/control/start") != llmOpsProxyTimeout {
		t.Fatalf("llm ops control timeout should keep startup wait")
	}
}

func TestLLMOpsReadProxyTimeoutIsShort(t *testing.T) {
	if llmOpsReadProxyTimeout > 5*time.Second {
		t.Fatalf("llm ops read timeout should be short enough for Viewer readiness, got %s", llmOpsReadProxyTimeout)
	}
	if llmOpsProxyTimeoutFor(http.MethodGet, "/v1/status") != llmOpsReadProxyTimeout {
		t.Fatalf("status timeout should use short read timeout")
	}
	if llmOpsProxyTimeoutFor(http.MethodGet, "/health") != llmOpsReadProxyTimeout {
		t.Fatalf("health timeout should use short read timeout")
	}
}

func TestHandleLLMOpsHealth_ProxiesWithoutBearerRequirement(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","daemon":"llm-mgmt"}`))
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsHealth(LLMOpsProxyOptions{BaseURL: upstream.URL})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/health", nil)
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var m map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m["daemon"] != "llm-mgmt" {
		t.Fatalf("body: %+v", m)
	}
}

func TestHandleLLMOpsStatus_TimesOutReadOnlyProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsStatus(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "testtok"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/status", nil)
	start := time.Now()
	h(rec, req)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("status proxy took too long: %s", time.Since(start))
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "upstream unreachable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestHandleLLMOpsStop_DefaultBody(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/control/stop" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsStop(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "x"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/stop", strings.NewReader(""))
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: %d", rec.Code)
	}
	if gotBody != `{"roles":["Chat","Worker"]}` {
		t.Fatalf("upstream body: %q", gotBody)
	}
}

func TestHandleLLMOpsStart_DefaultBody(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/control/start" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsStart(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "x"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/start", strings.NewReader(""))
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: %d", rec.Code)
	}
	if gotBody != `{"selection":"Worker"}` {
		t.Fatalf("upstream body: %q", gotBody)
	}
}

func TestHandleLLMOpsStart_ProxiesSelection(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/control/start" || r.Method != http.MethodPost {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("unexpected auth: %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok_all":true}`))
	}))
	t.Cleanup(upstream.Close)

	h := HandleLLMOpsStart(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "tok"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/llm-ops/start", strings.NewReader(`{"selection":"Heavy"}`))
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if gotBody != `{"selection":"Heavy"}` {
		t.Fatalf("upstream body: %q", gotBody)
	}
}

func TestHandleLLMOpsNotConfigured(t *testing.T) {
	h := HandleLLMOpsStatus(LLMOpsProxyOptions{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/status", nil)
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestLLMOpsIdleChatGate_BlocksWhenHeavyOrWildRunning(t *testing.T) {
	var requests []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/v1/status" {
			t.Fatalf("unexpected request while blocked: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"roles":{
				"Heavy":{"health_ok":true,"halted":false},
				"Wild":{"health_ok":false,"halted":true}
			},
			"memory":{"llm_by_role":{"Heavy":{"pid":2345},"Wild":{"pid":3456}}}
		}`))
	}))
	t.Cleanup(upstream.Close)

	gate := NewLLMOpsIdleChatGate(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "tok"})
	err := gate.PrepareIdleChatStart(context.Background())
	var busy *LLMOpsIdleChatBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("expected busy error, got %v", err)
	}
	if strings.Join(busy.Roles, ",") != "Heavy,Wild" {
		t.Fatalf("busy roles: %+v", busy.Roles)
	}
	if strings.Join(requests, ",") != "GET /v1/status" {
		t.Fatalf("requests: %+v", requests)
	}
}

func TestLLMOpsIdleChatGate_StatusTimesOutQuickly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)

	gate := NewLLMOpsIdleChatGate(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "tok"})
	start := time.Now()
	err := gate.PrepareIdleChatStart(context.Background())
	if err == nil {
		t.Fatal("PrepareIdleChatStart returned nil, want timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("PrepareIdleChatStart took too long: %s", time.Since(start))
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error=%v", err)
	}
}

func TestLLMOpsIdleChatGate_StopsHeavyWildThenStartsWorkerWhenStopped(t *testing.T) {
	var requests []string
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("unexpected auth: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(`{
				"roles":{
					"Heavy":{"health_ok":false,"halted":true},
					"Wild":{"health_ok":false,"halted":true}
				},
				"memory":{"llm_by_role":{"Heavy":{"pid":null},"Wild":{"pid":null}}}
			}`))
		case "/v1/control/stop", "/v1/control/start":
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	t.Cleanup(upstream.Close)

	gate := NewLLMOpsIdleChatGate(LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "tok"})
	if err := gate.PrepareIdleChatStart(context.Background()); err != nil {
		t.Fatalf("PrepareIdleChatStart: %v", err)
	}
	if strings.Join(requests, ",") != "GET /v1/status,POST /v1/control/stop,POST /v1/control/start" {
		t.Fatalf("requests: %+v", requests)
	}
	if strings.Join(bodies, ",") != `{"roles":["Heavy","Wild"]},{"selection":"Worker"}` {
		t.Fatalf("bodies: %+v", bodies)
	}
}
