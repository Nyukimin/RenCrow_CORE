package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
)

func TestOllamaCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	check := NewOllamaCheck(srv.URL)
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusOK {
		t.Errorf("expected OK, got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaCheck_Down(t *testing.T) {
	check := NewOllamaCheck("http://127.0.0.1:1") // 接続不可ポート
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusDown {
		t.Errorf("expected down, got %s", result.Status)
	}
}

func TestOllamaModelCheck_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	check := NewOllamaModelCheck(srv.URL, "Chat")
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusOK {
		t.Errorf("expected OK, got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaModelCheck_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	check := NewOllamaModelCheck(srv.URL, "nonexistent")
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusDown {
		t.Errorf("expected down, got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaModelsCheck_AllOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaPsResponse{
			Models: []struct {
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
			}{
				{Name: "Chat", ContextLength: 8192},
			},
		})
	}))
	defer srv.Close()

	check := NewOllamaModelsCheck(srv.URL, []ModelRequirement{
		{Name: "Chat", MaxContext: 8192},
	})
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusOK {
		t.Errorf("expected OK, got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaModelsCheck_ContextExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaPsResponse{
			Models: []struct {
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
			}{
				{Name: "Chat", ContextLength: 131072},
			},
		})
	}))
	defer srv.Close()

	check := NewOllamaModelsCheck(srv.URL, []ModelRequirement{
		{Name: "Chat", MaxContext: 8192},
	})
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusDown {
		t.Errorf("expected down (context exceeded), got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaModelsCheck_ModelNotLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaPsResponse{
			Models: []struct {
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
			}{},
		})
	}))
	defer srv.Close()

	check := NewOllamaModelsCheck(srv.URL, []ModelRequirement{
		{Name: "Chat", MaxContext: 8192},
	})
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusDown {
		t.Errorf("expected down (not loaded), got %s: %s", result.Status, result.Message)
	}
}

func TestOllamaModelsCheck_NoRequirements(t *testing.T) {
	check := NewOllamaModelsCheck("http://unused", nil)
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusOK {
		t.Errorf("expected OK for no requirements, got %s", result.Status)
	}
}

func TestOllamaModelsCheck_MaxContextZero_SkipsCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ollamaPsResponse{
			Models: []struct {
				Name          string `json:"name"`
				ContextLength int    `json:"context_length"`
			}{
				{Name: "Chat", ContextLength: 131072},
			},
		})
	}))
	defer srv.Close()

	check := NewOllamaModelsCheck(srv.URL, []ModelRequirement{
		{Name: "Chat", MaxContext: 0}, // 0 = チェックしない
	})
	result := check.Run(context.Background())

	if result.Status != domainhealth.StatusOK {
		t.Errorf("expected OK (MaxContext=0 skips check), got %s: %s", result.Status, result.Message)
	}
}
