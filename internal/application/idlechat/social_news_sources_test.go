package idlechat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchRedditHotSeedsPreservesCommunityAndPostURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "RenCrow") {
			t.Fatalf("User-Agent = %q, want RenCrow identifier", got)
		}
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
		<feed xmlns="http://www.w3.org/2005/Atom">
			<entry>
				<category term="technology" label="r/technology"/>
				<link href="https://www.reddit.com/r/technology/comments/abc123/example/"/>
				<title>Open source model reaches a new milestone</title>
			</entry>
			<entry>
				<category term="science" label="r/science"/>
				<link href="https://www.reddit.com/empty"/>
				<title>  </title>
			</entry>
		</feed>`))
	}))
	defer server.Close()

	got, err := fetchRedditHotSeedsFrom(server.Client(), server.URL, []string{"technology", "science"}, 10)
	if err != nil {
		t.Fatalf("fetchRedditHotSeedsFrom error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("seeds len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Title != "Open source model reaches a new milestone" ||
		got[0].Category != "social" ||
		got[0].Source != "Reddit r/technology" ||
		got[0].SourceType != "reddit" ||
		got[0].URL != "https://www.reddit.com/r/technology/comments/abc123/example/" {
		t.Fatalf("unexpected Reddit seed: %+v", got[0])
	}
}

func TestFetchXRecentSeedsUsesBearerTokenAndQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("query"); got != "AI lang:ja -is:retweet" {
			t.Fatalf("query = %q", got)
		}
		if got := r.URL.Query().Get("max_results"); got != "10" {
			t.Fatalf("max_results = %q, want API minimum 10", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "1900000000000000001", "text": "生成AIの新しい研究成果が公開されました"},
				{"id": "1900000000000000002", "text": "   "}
			]
		}`))
	}))
	defer server.Close()

	got, err := fetchXRecentSeedsFrom(server.Client(), server.URL, "secret-token", XNewsQuery{
		Name:     "X AI",
		Category: "tech",
		Query:    "AI lang:ja -is:retweet",
		Limit:    4,
	})
	if err != nil {
		t.Fatalf("fetchXRecentSeedsFrom error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("seeds len = %d, want 1: %+v", len(got), got)
	}
	if got[0].Title != "生成AIの新しい研究成果が公開されました" ||
		got[0].Category != "tech" ||
		got[0].Source != "X AI" ||
		got[0].SourceType != "x" ||
		got[0].URL != "https://x.com/i/web/status/1900000000000000001" {
		t.Fatalf("unexpected X seed: %+v", got[0])
	}
}

func TestMergeNewsSeedsDeduplicatesTitlesAndKeepsLimit(t *testing.T) {
	got := mergeNewsSeeds(3,
		[]NewsSeed{{Title: " A ", Source: "RSS"}, {Title: "B", Source: "RSS"}},
		[]NewsSeed{{Title: "A", Source: "Reddit"}, {Title: "C", Source: "X"}, {Title: "D", Source: "X"}},
	)
	if len(got) != 3 {
		t.Fatalf("seeds len = %d, want 3: %+v", len(got), got)
	}
	if got[0].Title != "A" || got[1].Title != "B" || got[2].Title != "C" {
		t.Fatalf("unexpected merged seeds: %+v", got)
	}
}
