package webgather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func TestSearXNGProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "RenCrow" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"url":"https://example.com","title":"Example","content":"Snippet","engine":"duckduckgo"}]}`))
	}))
	defer server.Close()
	resp, err := NewSearXNGProvider(server.URL).Search(context.Background(), modulewebgather.SearchRequest{Query: "RenCrow", Provider: "searxng", Limit: 5, Language: "ja"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].SourceEngine != "duckduckgo" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestSearXNGProviderClassifiesHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow", http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := NewSearXNGProvider(server.URL).Search(context.Background(), modulewebgather.SearchRequest{Query: "RenCrow", Provider: "searxng"})
	if err == nil {
		t.Fatal("expected error")
	}
	wgErr, ok := err.(*modulewebgather.Error)
	if !ok || wgErr.Code != modulewebgather.ErrRateLimited {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}
