package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
)

func TestProbeOllama_Success(t *testing.T) {
	tagsResp := ollamaTagsResponse{
		Models: []ollamaModelInfo{
			{Name: "gemma3:4b", Size: 4 * 1024 * 1024 * 1024},
			{Name: "qwen3.5:9b", Size: 9 * 1024 * 1024 * 1024},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(tagsResp)
	}))
	defer srv.Close()

	caps, err := ProbeOllama(context.Background(), srv.URL, map[string]int{"gemma3:4b": 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("expected 2 models, got %d", len(caps))
	}
	// 品質オーバーライドが反映される
	for _, c := range caps {
		if c.ModelName == "gemma3:4b" && c.Quality != 3 {
			t.Errorf("expected quality 3 for gemma3:4b, got %d", c.Quality)
		}
		if !c.Available {
			t.Errorf("expected available=true for %s", c.ModelName)
		}
	}
}

func TestProbeOllama_Unreachable(t *testing.T) {
	caps, err := ProbeOllama(context.Background(), "http://127.0.0.1:19999", nil)
	if err != nil {
		t.Fatalf("unreachable should not error: %v", err)
	}
	if len(caps) != 1 || caps[0].Available {
		t.Error("expected single unavailable entry for unreachable Ollama")
	}
}

func TestProbeAPIProvider(t *testing.T) {
	tests := []struct {
		name      string
		apiKey    string
		wantAvail bool
	}{
		{"with key", "sk-test", true},
		{"without key", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cap := ProbeAPIProvider("claude", tt.apiKey, "claude-sonnet-4-6", 5)
			if cap.Available != tt.wantAvail {
				t.Errorf("Available=%v, want %v", cap.Available, tt.wantAvail)
			}
			if cap.Quality != 5 {
				t.Errorf("Quality=%d, want 5", cap.Quality)
			}
		})
	}
}

func TestProbeAPIProvider_DefaultQuality(t *testing.T) {
	cap := ProbeAPIProvider("openai", "sk-test", "gpt-4o", 0)
	if cap.Quality != defaultQuality["openai"] {
		t.Errorf("expected default quality %d, got %d", defaultQuality["openai"], cap.Quality)
	}
}

func TestDetector_Detect(t *testing.T) {
	tagsResp := ollamaTagsResponse{
		Models: []ollamaModelInfo{
			{Name: "gemma3:4b"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tagsResp)
	}))
	defer srv.Close()

	d := &CapabilityDetector{
		ollamaBaseURL: srv.URL,
		coders: []coderProbeTarget{
			{providerName: "claude", apiKey: "sk-test", model: "claude-sonnet-4-6", quality: 5},
		},
		qualityMap: map[string]int{},
	}

	caps, err := d.Detect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caps.Platform.OS == "" {
		t.Error("expected non-empty OS")
	}
	if caps.NodeID == "" {
		t.Error("expected non-empty NodeID")
	}

	// Ollama + Claude の2種類以上
	if len(caps.LLMs) < 2 {
		t.Errorf("expected at least 2 LLMs, got %d", len(caps.LLMs))
	}

	// プロファイルが coder-high になること（Claude キーあり）
	profile := capability.DetermineProfile(caps)
	if profile != capability.ProfileCoderHigh {
		t.Errorf("profile = %q, want coder-high", profile)
	}
}
