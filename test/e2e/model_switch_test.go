//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func TestE2E_ViewerModelSwitch_RuntimeConfigStartAndSend(t *testing.T) {
	var managerCalls []string
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managerCalls = append(managerCalls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-token" && r.URL.Path != "/health" {
			t.Fatalf("missing management auth for %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/status":
			_, _ = w.Write([]byte(`{"roles":{"Chat":{"health_ok":true},"Heavy":{"health_ok":false},"Worker":{"health_ok":true},"Wild":{"health_ok":false}}}`))
		case "/v1/control/stop":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"roles":["Worker","Wild"]}` {
				t.Fatalf("unexpected stop body: %s", body)
			}
			_, _ = w.Write([]byte(`{"stopped":["Worker","Wild"],"halted":true}`))
		case "/v1/control/start":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"selection":"Heavy"}` {
				t.Fatalf("unexpected start body: %s", body)
			}
			_, _ = w.Write([]byte(`{"ok_all":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(manager.Close)

	received := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/viewer/runtime-config", viewer.HandleRuntimeConfig(viewer.DebugSystemOptions{
		LLMOpsConfigured: true,
		LLMOpsEnabled:    true,
		LLMOpsBaseURL:    manager.URL,
		LocalLLM: viewer.LocalLLMRuntimeConfig{
			Enabled:       true,
			Provider:      "local_openai",
			ChatBaseURL:   "http://127.0.0.1:18081",
			WorkerBaseURL: "http://127.0.0.1:18082",
			HeavyBaseURL:  "http://127.0.0.1:18083",
			WildBaseURL:   "http://127.0.0.1:18084",
			ChatModel:     "ChatRuntime",
			WorkerModel:   "WorkerRuntime",
			HeavyModel:    "HeavyRuntime",
			WildModel:     "WildRuntime",
			TimeoutSec:    120,
		},
	}))
	ops := viewer.LLMOpsProxyOptions{BaseURL: manager.URL, Token: "test-token"}
	mux.HandleFunc("/viewer/llm-ops/health", viewer.HandleLLMOpsHealth(ops))
	mux.HandleFunc("/viewer/llm-ops/status", viewer.HandleLLMOpsStatus(ops))
	mux.HandleFunc("/viewer/llm-ops/stop", viewer.HandleLLMOpsStop(ops))
	mux.HandleFunc("/viewer/llm-ops/start", viewer.HandleLLMOpsStart(ops))
	mux.HandleFunc("/viewer/send", viewer.HandleSend(func(_ context.Context, req viewer.SendRequest) (string, error) {
		received <- req.Message
		return "ok", nil
	}, nil))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	var cfg viewer.RuntimeConfig
	getJSON(t, server.URL+"/viewer/runtime-config", &cfg)
	if !cfg.LLMOpsEnabled || cfg.LocalLLM.HeavyBaseURL != "http://127.0.0.1:18083" || cfg.LocalLLM.HeavyModel != "HeavyRuntime" {
		t.Fatalf("unexpected runtime config: %+v", cfg)
	}

	postJSON(t, server.URL+"/viewer/llm-ops/stop", `{"roles":["Worker","Wild"]}`)
	postJSON(t, server.URL+"/viewer/llm-ops/start", `{"selection":"Heavy"}`)

	respBody := postJSON(t, server.URL+"/viewer/send", `{
		"message":"原因を調べて",
		"model_alias":"Heavy",
		"base_url":"http://127.0.0.1:18083",
		"model":"HeavyRuntime",
		"route_prefix":"/heavy"
	}`)
	if !strings.Contains(respBody, `"model":"HeavyRuntime"`) || !strings.Contains(respBody, `"route_prefix":"/heavy"`) {
		t.Fatalf("unexpected send response: %s", respBody)
	}
	select {
	case got := <-received:
		if got != "/heavy 原因を調べて" {
			t.Fatalf("viewer send routed %q, want /heavy prefix", got)
		}
	case <-time.After(time.Second):
		t.Fatal("viewer send handler was not called")
	}
	if len(managerCalls) < 2 {
		t.Fatalf("expected llm management calls, got %v", managerCalls)
	}
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status=%d body=%s", url, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func postJSON(t *testing.T, url, body string) string {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status=%d body=%s", url, resp.StatusCode, data)
	}
	return string(data)
}
