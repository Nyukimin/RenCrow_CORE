package webgather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestHTTPFetcherFetchesHTMLFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "RenCrow-WebGather") {
			t.Fatalf("unexpected user-agent: %s", ua)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>Example</title></head><body><article>Hello article body</article></body></html>"))
	}))
	defer server.Close()
	fetcher := NewHTTPFetcher()
	artifact, err := fetcher.Fetch(context.Background(), server.URL, modulewebgather.FetchPolicy{
		RequestTimeout: time.Second,
		MaxBodyBytes:   1024,
		MaxRedirects:   2,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if artifact.StatusCode != 200 || artifact.ContentType != "text/html" || artifact.RawBytes == 0 {
		t.Fatalf("unexpected artifact: %+v", artifact)
	}
}

func TestHTTPFetcherClassifies429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()
	_, err := NewHTTPFetcher().Fetch(context.Background(), server.URL, modulewebgather.FetchPolicy{
		RequestTimeout: time.Second,
		MaxBodyBytes:   1024,
		AllowLocalhost: true,
	})
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	wgErr, ok := err.(*modulewebgather.Error)
	if !ok || wgErr.Code != modulewebgather.ErrRateLimited {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}

func TestHTTPFetcherDetectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer server.Close()
	_, err := NewHTTPFetcher().Fetch(context.Background(), server.URL, modulewebgather.FetchPolicy{
		RequestTimeout: time.Second,
		MaxBodyBytes:   4,
		AllowLocalhost: true,
	})
	if err == nil {
		t.Fatal("expected body_too_large")
	}
	wgErr, ok := err.(*modulewebgather.Error)
	if !ok || wgErr.Code != modulewebgather.ErrBodyTooLarge {
		t.Fatalf("unexpected error: %T %v", err, err)
	}
}
