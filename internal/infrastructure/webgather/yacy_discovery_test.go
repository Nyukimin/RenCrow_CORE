package webgather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	modulewebgather "github.com/Nyukimin/picoclaw_multiLLM/modules/webgather"
)

func TestYaCyProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/yacysearch.json" || r.URL.Query().Get("query") != "RenCrow" {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"channels":[{"items":[{"title":"RenCrow","link":"https://example.com/rencrow","description":"local index result"}]}]}`))
	}))
	defer server.Close()

	resp, err := NewYaCyProvider(server.URL).Search(context.Background(), modulewebgather.SearchRequest{
		Query:    "RenCrow",
		Provider: "yacy",
		Limit:    5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].SourceEngine != "yacy" || resp.Results[0].URL != "https://example.com/rencrow" {
		t.Fatalf("unexpected yacy results: %+v", resp.Results)
	}
}
