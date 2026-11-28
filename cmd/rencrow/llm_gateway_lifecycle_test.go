package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestEnsureLLMGatewayDoesNotStartWhenGatewayIsLiveButMioAliasIsUnavailable(t *testing.T) {
	t.Setenv("RENCROW_LLM_CONFIG", filepath.Join(t.TempDir(), "missing-gateway-config.json"))
	liveProbeSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		case "/health/live":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
				t.Errorf("liveness request method=%q authorization=%q, want unauthenticated GET", r.Method, r.Header.Get("Authorization"))
			}
			select {
			case liveProbeSeen <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"status":"live","service":"rencrow-llm","runtime":"go"}`))
		default:
			t.Errorf("unexpected Gateway health path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL},
	})
	if status.Ready || status.AutoStartAttempted || status.AutoStarted || status.Process != nil {
		t.Fatalf("live Gateway with unavailable Mio alias must remain degraded without starting a child: %+v", status)
	}
	select {
	case <-liveProbeSeen:
	default:
		t.Fatal("Gateway process liveness was not checked after Mio alias readiness failed")
	}
	if !strings.Contains(strings.ToLower(status.Warning), "live") {
		t.Fatalf("warning=%q, want a warning that the Gateway process is live but Mio is unavailable", status.Warning)
	}
}

func TestLLMGatewayProcessLiveRejectsRedirectsAndOversizedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		handle func(http.ResponseWriter, *http.Request, chan<- struct{})
	}{
		{
			name: "redirect",
			handle: func(w http.ResponseWriter, r *http.Request, redirected chan<- struct{}) {
				if r.URL.Path == "/health/live" {
					http.Redirect(w, r, "/redirected-health", http.StatusFound)
					return
				}
				redirected <- struct{}{}
				_, _ = w.Write([]byte(`{"ok":true,"status":"live","service":"rencrow-llm","runtime":"go"}`))
			},
		},
		{
			name: "oversized response",
			handle: func(w http.ResponseWriter, _ *http.Request, _ chan<- struct{}) {
				body := `{"ok":true,"status":"live","service":"rencrow-llm","runtime":"go","padding":"` + strings.Repeat("x", 4096) + `"}`
				_, _ = w.Write([]byte(body))
			},
		},
		{
			name: "oversized trailing content",
			handle: func(w http.ResponseWriter, _ *http.Request, _ chan<- struct{}) {
				body := `{"ok":true,"status":"live","service":"rencrow-llm","runtime":"go"}` + strings.Repeat(" ", 4096)
				_, _ = w.Write([]byte(body))
			},
		},
		{
			name: "wrong service identity",
			handle: func(w http.ResponseWriter, _ *http.Request, _ chan<- struct{}) {
				_, _ = w.Write([]byte(`{"ok":true,"status":"live","service":"other-service","runtime":"go"}`))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redirected := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tc.handle(w, r, redirected)
			}))
			defer server.Close()

			if llmGatewayProcessLive(server.URL) {
				t.Fatal("invalid Gateway liveness response was accepted")
			}
			select {
			case <-redirected:
				t.Fatal("Gateway liveness client followed a redirect")
			default:
			}
		})
	}
}

func TestLLMGatewayProcessLiveDoesNotForwardURLCredentials(t *testing.T) {
	authorizationSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			select {
			case authorizationSeen <- struct{}{}:
			default:
			}
		}
		_, _ = w.Write([]byte(`{"ok":true,"status":"live","service":"rencrow-llm","runtime":"go"}`))
	}))
	defer server.Close()

	parsedURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse Gateway URL: %v", err)
	}
	parsedURL.User = url.UserPassword("health-user", "health-password")
	if !llmGatewayProcessLive(parsedURL.String()) {
		t.Fatal("valid Gateway liveness response was rejected")
	}
	select {
	case <-authorizationSeen:
		t.Fatal("Gateway liveness request forwarded credentials from the configured URL")
	default:
	}
}

func TestEnsureLLMGatewayStillAttemptsLocalStartWhenGatewayIsUnavailable(t *testing.T) {
	t.Setenv("RENCROW_LLM_CONFIG", filepath.Join(t.TempDir(), "missing-gateway-config.json"))
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	server.Close()

	status := ensureLLMGateway(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: baseURL},
	})
	if status.Ready || !status.AutoStartAttempted || status.AutoStarted || status.Process != nil {
		t.Fatalf("unavailable local Gateway should preserve the bounded auto-start attempt: %+v", status)
	}
}

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
