package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadLLMOpsTestConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("server:\n  port: 8080\n"+body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return LoadConfig(configPath)
}

func TestLLMOpsConfigDefaults(t *testing.T) {
	cfg, err := loadLLMOpsTestConfig(t, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fit := cfg.LLMOps.LLMFit
	if !fit.IsEnabled() {
		t.Fatal("llm_ops.llmfit should be enabled by default")
	}
	if fit.SystemTTL != "5m" || fit.ModelsTTL != "15m" || fit.HealthTTL != "30s" || fit.RequestTimeout != "5s" || fit.TopLimit != 20 {
		t.Fatalf("unexpected defaults: %+v", fit)
	}
	if len(fit.Nodes) != 0 {
		t.Fatalf("nodes should be empty by default: %+v", fit.Nodes)
	}
}

func TestLLMOpsConfigLoadsNodes(t *testing.T) {
	cfg, err := loadLLMOpsTestConfig(t, `llm_ops:
  llmfit:
    enabled: false
    system_ttl: 1m
    models_ttl: 2m
    health_ttl: 10s
    request_timeout: 3s
    top_limit: 5
    nodes:
      - id: " node-a "
        mode: " HTTP "
        endpoint: " http://192.168.x.x:8787/ "
      - id: local
        mode: cli
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	fit := cfg.LLMOps.LLMFit
	if fit.IsEnabled() {
		t.Fatal("enabled: false must be honored")
	}
	if fit.SystemTTL != "1m" || fit.ModelsTTL != "2m" || fit.HealthTTL != "10s" || fit.RequestTimeout != "3s" || fit.TopLimit != 5 {
		t.Fatalf("values not loaded: %+v", fit)
	}
	if len(fit.Nodes) != 2 {
		t.Fatalf("nodes=%d", len(fit.Nodes))
	}
	if fit.Nodes[0].ID != "node-a" || fit.Nodes[0].Mode != "http" || fit.Nodes[0].Endpoint != "http://192.168.x.x:8787" {
		t.Fatalf("http node not normalized: %+v", fit.Nodes[0])
	}
	if fit.Nodes[1].ID != "local" || fit.Nodes[1].Mode != "cli" || fit.Nodes[1].Endpoint != "" {
		t.Fatalf("cli node mismatch: %+v", fit.Nodes[1])
	}
}

func TestLLMOpsConfigValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "unknown mode", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        mode: ssh\n        endpoint: http://192.168.x.x:8787\n", want: "mode must be http or cli"},
		{name: "missing mode", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        endpoint: http://192.168.x.x:8787\n", want: "mode must be http or cli"},
		{name: "http without endpoint", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        mode: http\n", want: "endpoint is required"},
		{name: "http invalid endpoint", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        mode: http\n        endpoint: 'not a url'\n", want: "endpoint must be an http(s) URL"},
		{name: "cli with endpoint", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        mode: cli\n        endpoint: http://192.168.x.x:8787\n", want: "endpoint must be empty"},
		{name: "missing id", body: "llm_ops:\n  llmfit:\n    nodes:\n      - mode: cli\n", want: "id is required"},
		{name: "invalid id characters", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: 'a:b'\n        mode: cli\n", want: "id must match"},
		{name: "duplicate id", body: "llm_ops:\n  llmfit:\n    nodes:\n      - id: a\n        mode: cli\n      - id: a\n        mode: http\n        endpoint: http://192.168.x.x:8787\n", want: "duplicated"},
		{name: "bad system_ttl", body: "llm_ops:\n  llmfit:\n    system_ttl: soon\n", want: "system_ttl must be a positive duration"},
		{name: "negative models_ttl", body: "llm_ops:\n  llmfit:\n    models_ttl: -1m\n", want: "models_ttl must be a positive duration"},
		{name: "bad health_ttl", body: "llm_ops:\n  llmfit:\n    health_ttl: 0s\n", want: "health_ttl must be a positive duration"},
		{name: "bad request_timeout", body: "llm_ops:\n  llmfit:\n    request_timeout: 5\n", want: "request_timeout must be a positive duration"},
		{name: "top_limit too large", body: "llm_ops:\n  llmfit:\n    top_limit: 500\n", want: "top_limit must be between 1 and 200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadLLMOpsTestConfig(t, tt.body)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v want substring %q", err, tt.want)
			}
		})
	}
}

func TestLLMOpsConfigAcceptsHTTPSEndpointAndEmptyNodes(t *testing.T) {
	if _, err := loadLLMOpsTestConfig(t, "llm_ops:\n  llmfit:\n    nodes: []\n"); err != nil {
		t.Fatalf("empty nodes must be valid: %v", err)
	}
	cfg, err := loadLLMOpsTestConfig(t, "llm_ops:\n  llmfit:\n    nodes:\n      - id: remote\n        mode: http\n        endpoint: https://llmfit.example.internal:8787\n")
	if err != nil {
		t.Fatalf("https endpoint must be valid: %v", err)
	}
	if cfg.LLMOps.LLMFit.Nodes[0].Endpoint != "https://llmfit.example.internal:8787" {
		t.Fatalf("endpoint=%q", cfg.LLMOps.LLMFit.Nodes[0].Endpoint)
	}
}
