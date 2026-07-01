package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder_Embed_Success(t *testing.T) {
	// モックサーバー: /api/embeddings に対して embedding を返す
	want := []float32{0.1, 0.2, 0.3, 0.4}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" || r.Method != "POST" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "nomic-embed-text" {
			t.Errorf("expected model nomic-embed-text, got %v", req["model"])
		}

		resp := map[string]interface{}{
			"embedding": []float64{0.1, 0.2, 0.3, 0.4},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	got, err := embedder.Embed(context.Background(), "テストテキスト")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d dims, got %d", len(want), len(got))
	}
	for i, v := range want {
		if abs32(got[i]-v) > 1e-5 {
			t.Errorf("dim[%d]: expected %.4f, got %.4f", i, v, got[i])
		}
	}
}

func TestOllamaEmbedder_Embed_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	_, err := embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error on server 500, got nil")
	}
}

func TestOllamaEmbedder_Embed_EmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{"embedding": []float64{}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	embedder := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	_, err := embedder.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error on empty embedding, got nil")
	}
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
