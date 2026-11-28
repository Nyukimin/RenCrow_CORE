package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestEnsureLLMGatewayAcceptsHealthyGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("health path=%q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"}]}`))
	}))
	defer server.Close()

	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL},
	})
	if !status.Ready || status.AutoStartAttempted || status.Warning != "" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestEnsureLLMGatewayAcceptsHealthyGatewayWhenAutoStartDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("health path=%q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"}]}`))
	}))
	defer server.Close()

	autoStart := false
	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{AutoStart: &autoStart, BaseURL: server.URL},
	})
	if !status.Ready || status.AutoStartAttempted || status.Warning != "" || status.Process != nil {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestEnsureLLMGatewayDoesNotStartWhenAutoStartDisabled(t *testing.T) {
	t.Setenv("RENCROW_LLM_CONFIG", filepath.Join(t.TempDir(), "missing-gateway.json"))
	autoStart := false
	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{AutoStart: &autoStart, BaseURL: "http://127.0.0.1:1"},
	})
	if status.Ready || status.AutoStartAttempted || status.Process != nil {
		t.Fatalf("disabled auto-start must stop before launcher discovery: %+v", status)
	}
	if !strings.Contains(status.Warning, "managed by its supervisor") || strings.Contains(status.Warning, "does not exist") {
		t.Fatalf("warning=%q, want external-supervisor guidance without config lookup", status.Warning)
	}
}

func TestEnsureLLMGatewayDoesNotStartForUnhealthyHTTPAliasWhenAutoStartDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()
	t.Setenv("RENCROW_LLM_CONFIG", filepath.Join(t.TempDir(), "missing-gateway.json"))
	autoStart := false
	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{AutoStart: &autoStart, BaseURL: server.URL},
	})
	if status.Ready || status.AutoStartAttempted || status.Process != nil {
		t.Fatalf("unhealthy HTTP alias must not trigger launcher: %+v", status)
	}
	if !strings.Contains(status.Warning, "managed by its supervisor") {
		t.Fatalf("warning=%q, want external-supervisor guidance", status.Warning)
	}
}

func TestEnsureLLMGatewayOmittedAndEnabledAutoStartKeepLegacyDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		autoStart *bool
	}{
		{name: "omitted"},
		{name: "enabled", autoStart: boolPointer(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RENCROW_LLM_CONFIG", filepath.Join(t.TempDir(), "missing-gateway.json"))
			status := ensureLLMGateway(&config.Config{
				LLMGateway: config.LLMGatewayConfig{AutoStart: tc.autoStart, BaseURL: "http://127.0.0.1:1"},
			})
			if status.Ready || !status.AutoStartAttempted || status.Process != nil {
				t.Fatalf("legacy auto-start path changed: %+v", status)
			}
			if !strings.Contains(status.Warning, "no local Gateway config was found") {
				t.Fatalf("warning=%q, want legacy config discovery", status.Warning)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func TestLLMGatewayEmptyModelCatalogIsNotReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("health path=%q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	if llmGatewayHealthy(server.URL) {
		t.Fatal("empty model catalog must not be accepted as readiness")
	}
}

func TestEnsureLLMGatewayWarnsWhenRemoteGatewayCannotBeStarted(t *testing.T) {
	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: "http://192.0.2.1:1"},
	})
	if status.Ready || status.AutoStartAttempted {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !strings.Contains(status.Warning, "cannot start a remote Gateway") {
		t.Fatalf("warning=%q", status.Warning)
	}
}

func TestFindLLMGatewayLaunchPassesConfiguredListenAddress(t *testing.T) {
	t.Setenv("RENCROW_LLM_BIN", "")
	launch, err := findLLMGatewayLaunch("gateway.yaml", "127.0.0.1:18090")
	if err != nil {
		// The test machine may not have a local RenCrow_LLM binary or Go source
		// discoverable from its working directory.
		t.Skip(err)
	}
	joined := strings.Join(launch.Args, " ")
	if !strings.Contains(joined, "--listen 127.0.0.1:18090") {
		t.Fatalf("args=%q", joined)
	}
}
