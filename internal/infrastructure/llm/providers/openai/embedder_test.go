package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAIEmbedderEmbedSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "Embed" {
			t.Fatalf("unexpected model: %v", req["model"])
		}
		if req["input"] != "hello" {
			t.Fatalf("unexpected input: %v", req["input"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float64{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedderWithOptions("test-key", "Embed", srv.URL, time.Second)
	got, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(got))
	}
	if got[0] != float32(0.1) || got[1] != float32(0.2) || got[2] != float32(0.3) {
		t.Fatalf("unexpected embedding: %#v", got)
	}
}

func TestOpenAIEmbedderEmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": []float64{}}}})
	}))
	defer srv.Close()

	embedder := NewOpenAIEmbedderWithOptions("", "Embed", srv.URL, time.Second)
	if _, err := embedder.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("expected empty embedding error")
	}
}
