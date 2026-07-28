package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestProbeRenCrowLLMReturnsLogicalAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer gateway-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "mio"}, {"id": "coder4"}},
		})
	}))
	defer server.Close()

	caps, err := ProbeRenCrowLLM(context.Background(), server.URL, "gateway-token", map[string]int{"coder4": 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 2 || caps[0].ProviderName != "rencrow_llm" || caps[0].ModelName != "mio" {
		t.Fatalf("caps=%+v", caps)
	}
	if caps[1].ModelName != "coder4" || caps[1].Quality != 5 {
		t.Fatalf("coder4=%+v", caps[1])
	}
}

func TestProbeRenCrowLLMReportsUnavailable(t *testing.T) {
	caps, err := ProbeRenCrowLLM(context.Background(), "http://127.0.0.1:1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 || caps[0].Available || caps[0].ProviderName != "rencrow_llm" {
		t.Fatalf("caps=%+v", caps)
	}
}

func TestDetectorUsesGatewayConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"id": "worker"}},
		})
	}))
	defer server.Close()

	detector := NewCapabilityDetector(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL},
	})
	caps, err := detector.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.NodeID == "" || caps.Platform.OS == "" || len(caps.LLMs) != 1 || caps.LLMs[0].ModelName != "worker" {
		t.Fatalf("caps=%+v", caps)
	}
}
