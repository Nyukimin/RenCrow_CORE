package webgather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestArticleReaderFetcherReturnsValidatedArticleBodyAndProvenance(t *testing.T) {
	originalURL := "https://openai.com/index/example-article/"
	body := strings.Repeat("Independent testing paragraph with complete official details. ", 6) + "\n\n## Final section\n\nFinal paragraph with a [source link](https://openai.com/safety/)."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/http://openai.com/index/example-article/" {
			t.Fatalf("unexpected reader request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("Title: Example article\n\nURL Source: http://openai.com/index/example-article/\n\nMarkdown Content:\n" + body))
	}))
	defer server.Close()

	fetcher := NewArticleReaderFetcher(ArticleReaderFetcherConfig{
		Enabled: true, EndpointPrefix: server.URL + "/http://", AllowedSourceHosts: []string{"openai.com"}, TimeoutMS: 5000,
	})
	policy := modulewebgather.DefaultFetchPolicy()
	policy.AllowLocalhost = true
	artifact, err := fetcher.Fetch(context.Background(), originalURL, policy)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	text := string(artifact.Body)
	if strings.Contains(text, "Title: Example") || strings.Contains(text, "URL Source:") || strings.Contains(text, "Markdown Content:") {
		t.Fatalf("reader envelope leaked into article body: %q", text)
	}
	if !strings.Contains(text, "Final section") || !strings.HasSuffix(text, "Final paragraph with a source link.") {
		t.Fatalf("article body was altered or truncated: %q", text)
	}
	if artifact.OriginalURL != originalURL || artifact.FinalURL != originalURL || artifact.ProviderName != ArticleReaderProviderName {
		t.Fatalf("unexpected artifact identity: %+v", artifact)
	}
	if artifact.Meta["article_fetch_url"] != server.URL+"/http://openai.com/index/example-article/" || artifact.Meta["article_original_url"] != originalURL {
		t.Fatalf("missing reader provenance: %#v", artifact.Meta)
	}
	if artifact.Meta["article_content_sha256"] != modulewebgather.SHA256Text(text) || artifact.FetchedAt.IsZero() {
		t.Fatalf("missing hash/time provenance: artifact=%+v", artifact)
	}
}

func TestArticleReaderFetcherRejectsSourceOutsideAllowlistBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	fetcher := NewArticleReaderFetcher(ArticleReaderFetcherConfig{
		Enabled: true, EndpointPrefix: server.URL + "/http://", AllowedSourceHosts: []string{"openai.com"}, TimeoutMS: 5000,
	})
	policy := modulewebgather.DefaultFetchPolicy()
	policy.AllowLocalhost = true
	_, err := fetcher.Fetch(context.Background(), "https://example.com/article", policy)
	var gatherErr *modulewebgather.Error
	if !errors.As(err, &gatherErr) || gatherErr.Code != modulewebgather.ErrBlockedByPolicy {
		t.Fatalf("expected blocked_by_policy, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("reader must not be called for an unlisted source host: requests=%d", requests)
	}
}

func TestArticleReaderFetcherRejectsMismatchedReaderSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("URL Source: http://example.com/wrong\n\nMarkdown Content:\n" + strings.Repeat("complete text ", 30)))
	}))
	defer server.Close()
	fetcher := NewArticleReaderFetcher(ArticleReaderFetcherConfig{
		Enabled: true, EndpointPrefix: server.URL + "/http://", AllowedSourceHosts: []string{"openai.com"}, TimeoutMS: 5000,
	})
	policy := modulewebgather.DefaultFetchPolicy()
	policy.AllowLocalhost = true
	_, err := fetcher.Fetch(context.Background(), "https://openai.com/index/expected/", policy)
	var gatherErr *modulewebgather.Error
	if !errors.As(err, &gatherErr) || gatherErr.Code != modulewebgather.ErrBlockedByPolicy {
		t.Fatalf("expected mismatched source to be blocked, got %v", err)
	}
}

func TestArticleReaderFetcherDisabledDoesNotFetch(t *testing.T) {
	fetcher := NewArticleReaderFetcher(ArticleReaderFetcherConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := fetcher.Fetch(ctx, "https://openai.com/index/example/", modulewebgather.DefaultFetchPolicy())
	var gatherErr *modulewebgather.Error
	if !errors.As(err, &gatherErr) || gatherErr.Code != modulewebgather.ErrFetchFailed {
		t.Fatalf("expected explicit disabled failure, got %v", err)
	}
}

func TestBasicExtractorPreservesArticleReaderParagraphs(t *testing.T) {
	body := strings.Repeat("First complete paragraph. ", 10) + "\n\nFinal complete paragraph."
	doc, err := NewBasicExtractor().Extract(context.Background(), modulewebgather.FetchArtifact{
		ContentType: "text/plain", ProviderName: ArticleReaderProviderName, Body: []byte(body),
	}, modulewebgather.DefaultExtractor)
	if err != nil {
		t.Fatalf("Extract failed: %v", err)
	}
	if !strings.Contains(doc.Text, "\n\n") || doc.Extractor != "jina_reader_markdown" {
		t.Fatalf("reader paragraphs or extractor provenance were lost: %+v", doc)
	}
}
