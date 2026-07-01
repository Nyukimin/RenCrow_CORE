package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	domainhealth "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/health"
)

func TestOpenAICompatibleChatCheck_UsesModelsReadinessPath(t *testing.T) {
	paths := make([]string, 0, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Chat","backend_model":"gpt-oss:120b"}]}`))
	}))
	defer srv.Close()

	check := NewOpenAICompatibleChatCheck("Chat", srv.URL, "Chat", "", 0)
	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusOK {
		t.Fatalf("status = %s, want ok; message=%s", result.Status, result.Message)
	}
	if result.Name != "local_llm_chat" {
		t.Fatalf("name = %q", result.Name)
	}
	if len(paths) != 1 || paths[0] != "/v1/models" {
		t.Fatalf("health check must use lightweight readiness path; paths=%v", paths)
	}
}

func TestOpenAICompatibleChatCheck_DegradedWhenAliasMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Chat"}]}`))
	}))
	defer srv.Close()

	check := NewOpenAICompatibleChatCheck("Worker", srv.URL, "Worker", "", 0)
	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusDegraded {
		t.Fatalf("status = %s, want degraded; message=%s", result.Status, result.Message)
	}
}

func TestOpenAICompatibleChatCheck_DownOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	check := NewOpenAICompatibleChatCheck("Worker", srv.URL, "Worker", "", 0)
	result := check.Run(context.Background())
	if result.Status != domainhealth.StatusDown {
		t.Fatalf("status = %s, want down", result.Status)
	}
}
