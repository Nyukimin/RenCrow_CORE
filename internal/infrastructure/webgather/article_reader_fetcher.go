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
	"regexp"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const ArticleReaderProviderName = "jina_reader"

// ArticleReaderFetcherConfig configures the optional third-party article
// reader. The source allowlist is mandatory when the fetcher is enabled.
type ArticleReaderFetcherConfig struct {
	Enabled            bool
	EndpointPrefix     string
	AllowedSourceHosts []string
	TimeoutMS          int
}

type ArticleReaderFetcher struct {
	cfg    ArticleReaderFetcherConfig
	client *http.Client
}

func NewArticleReaderFetcher(cfg ArticleReaderFetcherConfig) *ArticleReaderFetcher {
	return &ArticleReaderFetcher{cfg: cfg}
}

func (f *ArticleReaderFetcher) WithHTTPClient(client *http.Client) *ArticleReaderFetcher {
	if f != nil {
		f.client = client
	}
	return f
}

func (f *ArticleReaderFetcher) Fetch(ctx context.Context, rawURL string, policy modulewebgather.FetchPolicy) (modulewebgather.FetchArtifact, error) {
	if f == nil || !f.cfg.Enabled {
		return modulewebgather.FetchArtifact{}, modulewebgather.NewError(modulewebgather.ErrFetchFailed, "article reader is disabled")
	}
	policy = policy.WithDefaults()
	originalURL, err := modulewebgather.NormalizeURL(rawURL, false)
	if err != nil {
		return modulewebgather.FetchArtifact{}, err
	}
	original, err := url.Parse(originalURL)
	if err != nil || !strings.EqualFold(original.Scheme, "https") {
		return modulewebgather.FetchArtifact{}, modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article reader accepts only public HTTPS source URLs")
	}
	if !allowedArticleReaderSourceHost(original.Hostname(), f.cfg.AllowedSourceHosts) {
		return modulewebgather.FetchArtifact{}, modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article source host is not allowlisted for the reader")
	}
	readerURL, readerOrigin, err := buildArticleReaderURL(f.cfg.EndpointPrefix, originalURL, policy.AllowLocalhost)
	if err != nil {
		return modulewebgather.FetchArtifact{}, err
	}

	client := f.client
	if client == nil {
		client = &http.Client{}
	}
	copied := *client
	timeout := policy.RequestTimeout
	if configured := time.Duration(f.cfg.TimeoutMS) * time.Millisecond; configured > 0 && (timeout <= 0 || configured < timeout) {
		timeout = configured
	}
	copied.Timeout = timeout
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= policy.MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", policy.MaxRedirects)
		}
		if !strings.EqualFold(req.URL.Scheme+"://"+req.URL.Host, readerOrigin) {
			return modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article reader redirect left the configured origin")
		}
		if !policy.AllowLocalhost && modulewebgather.IsPrivateHost(req.URL.Hostname()) {
			return modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article reader redirect to a private host is blocked")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readerURL, nil)
	if err != nil {
		return modulewebgather.FetchArtifact{}, modulewebgather.WrapError(modulewebgather.ErrInvalidURL, "failed to build article reader request", err)
	}
	req.Header.Set("User-Agent", "RenCrow-NewsArticleReader/0.1 (+https://local.rencrow.invalid)")
	req.Header.Set("Accept", "text/plain,text/markdown;q=0.9,*/*;q=0.1")
	started := time.Now()
	resp, err := copied.Do(req)
	if err != nil {
		var urlErr *url.Error
		var netErr net.Error
		if (errors.As(err, &urlErr) && urlErr.Timeout()) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return modulewebgather.FetchArtifact{}, modulewebgather.WrapError(modulewebgather.ErrFetchTimeout, "article reader request timed out", err)
		}
		var gatherErr *modulewebgather.Error
		if errors.As(err, &gatherErr) {
			return modulewebgather.FetchArtifact{}, gatherErr
		}
		return modulewebgather.FetchArtifact{}, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "article reader request failed", err)
	}
	defer resp.Body.Close()
	artifact := modulewebgather.FetchArtifact{
		OriginalURL: originalURL, FinalURL: originalURL, StatusCode: resp.StatusCode,
		ContentType: "text/plain", Elapsed: time.Since(started), FetchedAt: time.Now().UTC(), ProviderName: ArticleReaderProviderName,
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return artifact, modulewebgather.NewError(modulewebgather.ErrRateLimited, "article reader returned 429")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return artifact, modulewebgather.NewError(modulewebgather.ErrHTTPStatus, fmt.Sprintf("article reader returned HTTP %d", resp.StatusCode))
	}
	var raw bytes.Buffer
	n, err := io.Copy(&raw, io.LimitReader(resp.Body, policy.MaxBodyBytes+1))
	if err != nil {
		return artifact, modulewebgather.WrapError(modulewebgather.ErrFetchFailed, "failed to read article reader response", err)
	}
	if n > policy.MaxBodyBytes {
		return artifact, modulewebgather.NewError(modulewebgather.ErrBodyTooLarge, "article reader response exceeded max_body_bytes")
	}
	articleText, reportedSource, err := extractArticleReaderBody(raw.String())
	if err != nil {
		return artifact, err
	}
	if !sameArticleReaderSource(originalURL, reportedSource) {
		return artifact, modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article reader response source does not match the requested article")
	}
	artifact.Body = []byte(articleText)
	artifact.RawBytes = n
	artifact.Meta = map[string]any{
		"article_original_url":   originalURL,
		"article_fetch_url":      readerURL,
		"article_content_sha256": modulewebgather.SHA256Text(articleText),
		"article_reader_source":  reportedSource,
	}
	return artifact, nil
}

func allowedArticleReaderSourceHost(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, candidate := range allowed {
		if host != "" && host == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func buildArticleReaderURL(endpointPrefix string, originalURL string, allowLocalhost bool) (string, string, error) {
	prefix := strings.TrimSpace(endpointPrefix)
	endpoint, err := url.Parse(prefix)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return "", "", modulewebgather.NewError(modulewebgather.ErrInvalidURL, "article reader endpoint_prefix must be an absolute URL")
	}
	if endpoint.Scheme != "https" && !(allowLocalhost && endpoint.Scheme == "http" && modulewebgather.IsPrivateHost(endpoint.Hostname())) {
		return "", "", modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "article reader endpoint must use HTTPS")
	}
	if !allowLocalhost && modulewebgather.IsPrivateHost(endpoint.Hostname()) {
		return "", "", modulewebgather.NewError(modulewebgather.ErrBlockedByPolicy, "private article reader endpoint is blocked")
	}
	withoutScheme := strings.TrimPrefix(strings.TrimPrefix(originalURL, "https://"), "http://")
	readerURL := prefix + withoutScheme
	parsedReader, err := url.Parse(readerURL)
	if err != nil || parsedReader.Scheme == "" || parsedReader.Host == "" || !strings.EqualFold(parsedReader.Host, endpoint.Host) {
		return "", "", modulewebgather.NewError(modulewebgather.ErrInvalidURL, "failed to build article reader URL")
	}
	return readerURL, endpoint.Scheme + "://" + endpoint.Host, nil
}

var (
	articleReaderImageRE = regexp.MustCompile(`!\[[^\]]*\]\([^\)]+\)`)
	articleReaderLinkRE  = regexp.MustCompile(`\[([^\]]+)\]\([^\)]+\)`)
	articleReaderHeadRE  = regexp.MustCompile(`^#{1,6}\s+`)
)

func extractArticleReaderBody(raw string) (string, string, error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")
	reportedSource := ""
	contentStart := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "URL Source:") {
			reportedSource = strings.TrimSpace(strings.TrimPrefix(trimmed, "URL Source:"))
		}
		if trimmed == "Markdown Content:" {
			contentStart = i + 1
			break
		}
	}
	if reportedSource == "" || contentStart < 0 {
		return "", "", modulewebgather.NewError(modulewebgather.ErrExtractFailed, "article reader response is missing its source envelope")
	}
	cleaned := make([]string, 0, len(lines)-contentStart)
	blank := false
	for _, line := range lines[contentStart:] {
		line = strings.TrimSpace(line)
		line = articleReaderImageRE.ReplaceAllString(line, "")
		line = articleReaderLinkRE.ReplaceAllString(line, "$1")
		line = articleReaderHeadRE.ReplaceAllString(line, "")
		line = strings.ReplaceAll(line, "**", "")
		line = strings.ReplaceAll(line, "__", "")
		line = strings.ReplaceAll(line, "`", "")
		line = strings.TrimSpace(line)
		if line == "" {
			if len(cleaned) > 0 && !blank {
				cleaned = append(cleaned, "")
				blank = true
			}
			continue
		}
		cleaned = append(cleaned, line)
		blank = false
	}
	articleText := strings.TrimSpace(strings.Join(cleaned, "\n"))
	if articleText == "" {
		return "", "", modulewebgather.NewError(modulewebgather.ErrEmptyContent, "article reader returned an empty article body")
	}
	return articleText, reportedSource, nil
}

func sameArticleReaderSource(originalURL string, reportedURL string) bool {
	original, errOriginal := url.Parse(strings.TrimSpace(originalURL))
	reported, errReported := url.Parse(strings.TrimSpace(reportedURL))
	if errOriginal != nil || errReported != nil || original.Hostname() == "" || reported.Hostname() == "" {
		return false
	}
	normalizePath := func(value string) string {
		if value == "" {
			return "/"
		}
		if value != "/" {
			value = strings.TrimSuffix(value, "/")
		}
		return value
	}
	return strings.EqualFold(original.Hostname(), reported.Hostname()) &&
		normalizePath(original.EscapedPath()) == normalizePath(reported.EscapedPath()) &&
		original.RawQuery == reported.RawQuery
}
