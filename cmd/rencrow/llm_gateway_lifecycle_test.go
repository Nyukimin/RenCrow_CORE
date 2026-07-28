package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestEnsureLLMGatewayAcceptsHealthyGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL},
	})
	if !status.Ready || status.AutoStartAttempted || status.Warning != "" {
		t.Fatalf("unexpected status: %+v", status)
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
