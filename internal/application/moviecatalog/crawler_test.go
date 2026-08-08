package moviecatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPCrawlerWithoutURLIsUnavailable(t *testing.T) {
	crawler := NewHTTPCrawler("", 0)
	_, err := crawler.Crawl(context.Background(), CrawlerRequest{URL: "https://eiga.com/movie/1/", ArtifactDir: t.TempDir()})
	if !errors.Is(err, ErrCrawlerUnavailable) {
		t.Fatalf("expected unavailable error, got %v", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestHTTPCrawlerDownloadsAndVerifiesArtifact(t *testing.T) {
	artifact := []byte(`{"kind":"movie","movie_id":"1","title":"Movie","url":"https://eiga.com/movie/1/"}` + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/movie-catalog/crawls":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected crawler method: %s", r.Method)
			}
			var request crawlerRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode crawler request: %v", err)
			}
			if request.Kind != "movie" || request.SeedURL != "https://eiga.com/movie/1/" || request.Query != "" {
				t.Fatalf("unexpected crawler request: %+v", request)
			}
			_ = json.NewEncoder(w).Encode(crawlerResponsePayload{
				JobID:          "job-1",
				State:          "succeeded",
				ArtifactURL:    "/artifact.jsonl",
				ArtifactSHA256: sha256Hex(artifact),
				ArtifactBytes:  int64(len(artifact)),
			})
		case "/artifact.jsonl":
			w.Header().Set("Content-Type", "application/x-ndjson")
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	artifactDir := t.TempDir()
	result, err := NewHTTPCrawler(server.URL, 5*time.Second).Crawl(context.Background(), CrawlerRequest{
		Kind:        "movie",
		URL:         "https://eiga.com/movie/1/",
		MaxPages:    2,
		ArtifactDir: artifactDir,
	})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	defer os.Remove(result.ArtifactPath)
	if result.JobID != "job-1" || result.ArtifactBytes != int64(len(artifact)) || result.ArtifactSHA256 != sha256Hex(artifact) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.HasPrefix(result.ArtifactPath, filepath.Clean(artifactDir)) {
		t.Fatalf("artifact escaped staging dir: %s", result.ArtifactPath)
	}
}

func TestHTTPCrawlerSendsQueryWithoutSeedURL(t *testing.T) {
	artifact := []byte(`{"kind":"movie","movie_id":"2","title":"Query Movie"}` + "\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/movie-catalog/crawls":
			var request crawlerRequestPayload
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatalf("decode crawler request: %v", err)
			}
			if request.SeedURL != "" || request.Query != "マージン・コール" {
				t.Fatalf("expected query-only crawler request, got %+v", request)
			}
			_ = json.NewEncoder(w).Encode(crawlerResponsePayload{
				JobID:          "job-query",
				State:          "succeeded",
				ArtifactURL:    "/artifact.jsonl",
				ArtifactSHA256: sha256Hex(artifact),
				ArtifactBytes:  int64(len(artifact)),
			})
		case "/artifact.jsonl":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewHTTPCrawler(server.URL, 5*time.Second).Crawl(context.Background(), CrawlerRequest{
		Kind:        "movie",
		Query:       " マージン・コール ",
		ArtifactDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("crawl query: %v", err)
	}
	defer os.Remove(result.ArtifactPath)
	if result.JobID != "job-query" {
		t.Fatalf("unexpected query crawl result: %+v", result)
	}
}

func TestHTTPCrawlerPreservesAmbiguousCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/movie-catalog/crawls" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(crawlerResponsePayload{
			ErrorCode: "D0_RESOLUTION_AMBIGUOUS",
			Message:   "choose a candidate",
			Candidates: []CrawlerCandidate{{
				Kind:  "movie",
				Label: "候補作品",
				URL:   "https://eiga.com/movie/101/",
			}},
		})
	}))
	defer server.Close()

	_, err := NewHTTPCrawler(server.URL, 5*time.Second).Crawl(context.Background(), CrawlerRequest{
		Kind:        "movie",
		Query:       "候補作品",
		ArtifactDir: t.TempDir(),
	})
	var serviceErr *CrawlerServiceError
	if !errors.As(err, &serviceErr) {
		t.Fatalf("expected crawler service error, got %v", err)
	}
	if serviceErr.StatusCode != http.StatusConflict || serviceErr.Code != "D0_RESOLUTION_AMBIGUOUS" {
		t.Fatalf("unexpected service error: %+v", serviceErr)
	}
	if len(serviceErr.Candidates) != 1 || serviceErr.Candidates[0].Label != "候補作品" || serviceErr.Candidates[0].URL != "https://eiga.com/movie/101/" {
		t.Fatalf("candidates were not preserved: %+v", serviceErr.Candidates)
	}
}

func TestHTTPCrawlerRequiresExactlyOneSeed(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	_, err := NewHTTPCrawler(server.URL, 5*time.Second).Crawl(context.Background(), CrawlerRequest{
		Kind:        "movie",
		URL:         "https://eiga.com/movie/1/",
		Query:       "作品名",
		ArtifactDir: t.TempDir(),
	})
	if !errors.Is(err, ErrCrawlerProtocol) {
		t.Fatalf("expected protocol error for both URL and query, got %v", err)
	}
}
