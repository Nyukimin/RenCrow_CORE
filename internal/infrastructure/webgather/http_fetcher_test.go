package webgather

import (
	"context"
	"io"
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

func TestHTTPFetcherUsesNHKAuthorizationSessionForFullArticle(t *testing.T) {
	const articleURL = "https://news.web.nhk/newsweb/na/na-k10015196951000"
	requests := []string{}
	fetcher := NewHTTPFetcher()
	fetcher.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.String())
		if r.URL.Path == "/tix/build_authorize" {
			if got := r.URL.Query().Get("redirect_uri"); got != articleURL {
				t.Fatalf("unexpected redirect_uri: %s", got)
			}
			return &http.Response{
				StatusCode: http.StatusFound,
				Header: http.Header{
					"Location":   []string{articleURL},
					"Set-Cookie": []string{"nhk-session=full; Path=/; Secure; HttpOnly"},
				},
				Body:    io.NopCloser(strings.NewReader("")),
				Request: r,
			}, nil
		}
		if r.URL.String() != articleURL {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		cookie, err := r.Cookie("nhk-session")
		if err != nil || cookie.Value != "full" {
			t.Fatalf("authorization cookie missing from article request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader("<html><body><main><p><span class=\"c-part\">Full NHK article body.</span></p></main></body></html>")),
			Request:    r,
		}, nil
	})}

	artifact, err := fetcher.Fetch(context.Background(), articleURL, modulewebgather.DefaultFetchPolicy())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(requests) != 2 || !strings.Contains(requests[0], "/tix/build_authorize?") || requests[1] != articleURL {
		t.Fatalf("unexpected request sequence: %#v", requests)
	}
	if artifact.OriginalURL != articleURL || artifact.FinalURL != articleURL || !strings.Contains(string(artifact.Body), "Full NHK article body") {
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

func TestLooksLikeBotChallengeRequiresChallengeEvidence(t *testing.T) {
	article := `<html><head><title>Security article</title></head><body><article>` +
		strings.Repeat("A normal article discussing Cloudflare and captcha defenses. ", 3000) +
		`</article></body></html>`
	if looksLikeBotChallenge("text/html", []byte(article)) {
		t.Fatal("a full article mentioning bot-defense terms must not be blocked")
	}
	challenge := `<html><head><title>Just a moment...</title></head><body><script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1"></script></body></html>`
	if !looksLikeBotChallenge("text/html", []byte(challenge)) {
		t.Fatal("Cloudflare challenge structure must be blocked")
	}
}
