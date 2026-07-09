package webgather

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type HTTPFetcher struct {
	client *http.Client
}

func NewHTTPFetcher() *HTTPFetcher {
	return &HTTPFetcher{}
}

func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string, policy modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	policy = policy.WithDefaults()
	start := time.Now()
	redirects := []string{}
	client := f.client
	if client == nil {
		client = &http.Client{}
	}
	copied := *client
	copied.Timeout = policy.RequestTimeout
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= policy.MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", policy.MaxRedirects)
		}
		if !policy.AllowLocalhost && modulewebgather.IsPrivateHost(req.URL.Hostname()) {
			return modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "redirect to private or localhost URL is blocked")
		}
		redirects = append(redirects, req.URL.String())
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return modulewebgather.FetchArtifact{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build request", err)
	}
	req.Header.Set("User-Agent", "RenCrow-WebGather/0.1 (+https://local.rencrow.invalid)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,text/markdown,application/json,application/ld+json;q=0.9,*/*;q=0.1")
	resp, err := copied.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return modulewebgather.FetchArtifact{Elapsed: elapsed}, modulewebgather.WrapError(modulewebgather.ErrFetchTimeout, "request timed out", err)
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return modulewebgather.FetchArtifact{Elapsed: elapsed}, modulewebgather.WrapError(modulewebgather.ErrFetchTimeout, "request timed out", err)
		}
		var wgErr *modulewebgather.Error
		if errors.As(err, &wgErr) {
			return modulewebgather.FetchArtifact{Elapsed: elapsed}, wgErr
		}
		return modulewebgather.FetchArtifact{Elapsed: elapsed}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "request failed", err)
	}
	defer resp.Body.Close()
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	artifact := modulewebgather.FetchArtifact{
		OriginalURL:  rawURL,
		FinalURL:     resp.Request.URL.String(),
		StatusCode:   resp.StatusCode,
		ContentType:  contentType,
		Elapsed:      elapsed,
		Redirects:    redirects,
		FetchedAt:    time.Now().UTC(),
		ProviderName: "http",
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return artifact, modulewebgather.NewError(modulewebgather.ErrRateLimited, "remote server returned 429")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return artifact, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("remote server returned HTTP %d", resp.StatusCode))
	}
	var buf bytes.Buffer
	limited := io.LimitReader(resp.Body, policy.MaxBodyBytes+1)
	n, err := io.Copy(&buf, limited)
	if err != nil {
		return artifact, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to read response body", err)
	}
	if n > policy.MaxBodyBytes {
		return artifact, modulewebgather.NewError(modulewebgather.ErrBodyTooLarge, "response body exceeded max_body_bytes")
	}
	artifact.Body = buf.Bytes()
	artifact.RawBytes = int64(len(artifact.Body))
	if looksLikeBotChallenge(contentType, artifact.Body) {
		return artifact, modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "response appears to be a bot challenge")
	}
	return artifact, nil
}

func looksLikeBotChallenge(contentType string, body []byte) bool {
	if !strings.Contains(contentType, "html") || len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	markers := []string{"captcha", "bot challenge", "cloudflare", "verify you are human"}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
