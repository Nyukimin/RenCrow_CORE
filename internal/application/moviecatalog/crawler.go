package moviecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultCrawlerTimeout = 90 * time.Second
	maxCrawlerResponse    = 2 << 20
	maxCrawlerArtifact    = 64 << 20
	crawlerPollInterval   = 250 * time.Millisecond
)

var (
	// ErrCrawlerUnavailable means that CORE has no configured crawler sidecar.
	// It is intentionally distinct from a crawler execution failure so callers
	// can return a stable 503/error code without pretending a local fallback ran.
	ErrCrawlerUnavailable = errors.New("movie catalog crawler unavailable")
	ErrCrawlerProtocol    = errors.New("movie catalog crawler protocol error")
)

type CrawlerRequest struct {
	RequestID                string
	Kind                     string
	URL                      string
	MaxPages                 int
	FollowLinks              bool
	IncludePersonFilmography bool
	Delay                    time.Duration
	ArtifactDir              string
}

type CrawlResult struct {
	JobID          string
	Status         string
	ArtifactURL    string
	ArtifactPath   string
	ArtifactSHA256 string
	ArtifactBytes  int64
	Output         string
}

type Crawler interface {
	Crawl(ctx context.Context, request CrawlerRequest) (CrawlResult, error)
}

type CrawlerServiceError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *CrawlerServiceError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if strings.TrimSpace(e.Code) == "" {
		return message
	}
	return strings.TrimSpace(e.Code) + ": " + message
}

func (e *CrawlerServiceError) CrawlerErrorCode() string {
	return strings.TrimSpace(e.Code)
}

type HTTPCrawler struct {
	baseURL    string
	httpClient *http.Client
}

func NewHTTPCrawler(baseURL string, timeout time.Duration) *HTTPCrawler {
	if timeout <= 0 {
		timeout = defaultCrawlerTimeout
	}
	return &HTTPCrawler{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

func ResolveCrawlerBaseURL() string {
	return strings.TrimSpace(os.Getenv("RENCROW_MOVIE_CATALOG_CRAWLER_URL"))
}

func NewConfiguredCrawler(timeout time.Duration) Crawler {
	return NewHTTPCrawler(ResolveCrawlerBaseURL(), timeout)
}

type crawlerRequestPayload struct {
	RequestID                string  `json:"request_id,omitempty"`
	Kind                     string  `json:"kind"`
	SeedURL                  string  `json:"seed_url"`
	MaxPages                 int     `json:"max_pages"`
	FollowLinks              bool    `json:"follow_links"`
	IncludePersonFilmography bool    `json:"include_person_filmography"`
	DelaySec                 float64 `json:"delay_sec"`
}

type crawlerResponsePayload struct {
	JobID          string `json:"job_id"`
	State          string `json:"state"`
	Status         string `json:"status"`
	StatusURL      string `json:"status_url"`
	ArtifactURL    string `json:"artifact_url"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ArtifactBytes  int64  `json:"artifact_bytes"`
	Output         string `json:"output"`
	ErrorCode      string `json:"error_code"`
	Message        string `json:"message"`
}

func (c *HTTPCrawler) Crawl(ctx context.Context, request CrawlerRequest) (CrawlResult, error) {
	if c == nil || strings.TrimSpace(c.baseURL) == "" {
		return CrawlResult{}, fmt.Errorf("%w: set RENCROW_MOVIE_CATALOG_CRAWLER_URL", ErrCrawlerUnavailable)
	}
	if strings.TrimSpace(request.URL) == "" {
		return CrawlResult{}, fmt.Errorf("%w: seed URL is required", ErrCrawlerProtocol)
	}
	artifactDir := strings.TrimSpace(request.ArtifactDir)
	if artifactDir == "" {
		return CrawlResult{}, fmt.Errorf("%w: artifact directory is required", ErrCrawlerProtocol)
	}
	if request.MaxPages <= 0 {
		request.MaxPages = 1
	}
	if request.Delay < 0 {
		request.Delay = 0
	}

	payload := crawlerRequestPayload{
		RequestID:                strings.TrimSpace(request.RequestID),
		Kind:                     strings.TrimSpace(request.Kind),
		SeedURL:                  strings.TrimSpace(request.URL),
		MaxPages:                 request.MaxPages,
		FollowLinks:              request.FollowLinks,
		IncludePersonFilmography: request.IncludePersonFilmography,
		DelaySec:                 request.Delay.Seconds(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CrawlResult{}, fmt.Errorf("encode movie catalog crawler request: %w", err)
	}

	response, result, err := c.post(ctx, c.baseURL+"/v1/movie-catalog/crawls", body)
	if err != nil {
		return CrawlResult{}, err
	}
	if strings.TrimSpace(result.StatusURL) != "" && !crawlerResultTerminal(result) {
		result, err = c.wait(ctx, result)
		if err != nil {
			return CrawlResult{}, err
		}
	}
	state := strings.ToLower(strings.TrimSpace(result.State))
	status := strings.ToLower(strings.TrimSpace(result.Status))
	if state == "failed" || status == "failed" || status == "error" {
		return CrawlResult{}, &CrawlerServiceError{StatusCode: response.StatusCode, Code: result.ErrorCode, Message: result.Message}
	}
	if state != "" && state != "succeeded" && state != "completed" || status != "" && status != "ok" && status != "succeeded" && status != "completed" {
		return CrawlResult{}, fmt.Errorf("%w: state=%q status=%q", ErrCrawlerProtocol, result.State, result.Status)
	}
	if strings.TrimSpace(result.ArtifactURL) == "" {
		return CrawlResult{}, fmt.Errorf("%w: artifact_url is missing", ErrCrawlerProtocol)
	}

	artifactPath, sum, size, err := c.downloadArtifact(ctx, result.ArtifactURL, artifactDir)
	if err != nil {
		return CrawlResult{}, err
	}
	if expected := strings.ToLower(strings.TrimSpace(result.ArtifactSHA256)); expected != "" && expected != sum {
		return CrawlResult{}, fmt.Errorf("%w: artifact sha256 mismatch (expected=%s actual=%s)", ErrCrawlerProtocol, expected, sum)
	}
	if result.ArtifactBytes > 0 && result.ArtifactBytes != size {
		return CrawlResult{}, fmt.Errorf("%w: artifact size mismatch (expected=%d actual=%d)", ErrCrawlerProtocol, result.ArtifactBytes, size)
	}
	return CrawlResult{
		JobID:          result.JobID,
		Status:         firstNonEmpty(result.Status, result.State, "succeeded"),
		ArtifactURL:    result.ArtifactURL,
		ArtifactPath:   artifactPath,
		ArtifactSHA256: sum,
		ArtifactBytes:  size,
		Output:         strings.TrimSpace(result.Output),
	}, nil
}

func (c *HTTPCrawler) post(ctx context.Context, endpoint string, body []byte) (*http.Response, crawlerResponsePayload, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, crawlerResponsePayload{}, fmt.Errorf("create movie catalog crawler request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, crawlerResponsePayload{}, fmt.Errorf("movie catalog crawler request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxCrawlerResponse+1))
	if err != nil {
		return response, crawlerResponsePayload{}, fmt.Errorf("read movie catalog crawler response: %w", err)
	}
	if len(payload) > maxCrawlerResponse {
		return response, crawlerResponsePayload{}, fmt.Errorf("%w: response exceeds %d bytes", ErrCrawlerProtocol, maxCrawlerResponse)
	}
	var result crawlerResponsePayload
	if len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, &result); err != nil {
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				return response, crawlerResponsePayload{}, &CrawlerServiceError{StatusCode: response.StatusCode, Message: strings.TrimSpace(string(payload))}
			}
			return response, crawlerResponsePayload{}, fmt.Errorf("%w: decode response: %v", ErrCrawlerProtocol, err)
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, result, &CrawlerServiceError{StatusCode: response.StatusCode, Code: result.ErrorCode, Message: result.Message}
	}
	return response, result, nil
}

func (c *HTTPCrawler) wait(ctx context.Context, initial crawlerResponsePayload) (crawlerResponsePayload, error) {
	statusURL, err := resolveCrawlerURL(c.baseURL, initial.StatusURL)
	if err != nil {
		return crawlerResponsePayload{}, fmt.Errorf("%w: invalid status_url: %v", ErrCrawlerProtocol, err)
	}
	result := initial
	ticker := time.NewTicker(crawlerPollInterval)
	defer ticker.Stop()
	for {
		if crawlerResultTerminal(result) {
			state := strings.ToLower(strings.TrimSpace(result.State))
			status := strings.ToLower(strings.TrimSpace(result.Status))
			if state == "failed" || status == "failed" || status == "error" {
				return crawlerResponsePayload{}, &CrawlerServiceError{StatusCode: http.StatusBadGateway, Code: result.ErrorCode, Message: result.Message}
			}
			return result, nil
		}
		select {
		case <-ctx.Done():
			return crawlerResponsePayload{}, ctx.Err()
		case <-ticker.C:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			return crawlerResponsePayload{}, fmt.Errorf("create crawler status request: %w", err)
		}
		request.Header.Set("Accept", "application/json")
		response, err := c.httpClient.Do(request)
		if err != nil {
			return crawlerResponsePayload{}, fmt.Errorf("movie catalog crawler status request failed: %w", err)
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxCrawlerResponse+1))
		response.Body.Close()
		if readErr != nil {
			return crawlerResponsePayload{}, fmt.Errorf("read crawler status response: %w", readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return crawlerResponsePayload{}, &CrawlerServiceError{StatusCode: response.StatusCode, Message: strings.TrimSpace(string(payload))}
		}
		if err := json.Unmarshal(payload, &result); err != nil {
			return crawlerResponsePayload{}, fmt.Errorf("%w: decode status response: %v", ErrCrawlerProtocol, err)
		}
	}
}

func crawlerResultTerminal(result crawlerResponsePayload) bool {
	state := strings.ToLower(strings.TrimSpace(result.State))
	status := strings.ToLower(strings.TrimSpace(result.Status))
	return state == "succeeded" || state == "completed" || state == "failed" ||
		status == "ok" || status == "succeeded" || status == "completed" || status == "failed" || status == "error"
}

func (c *HTTPCrawler) downloadArtifact(ctx context.Context, rawURL string, artifactDir string) (string, string, int64, error) {
	artifactURL, err := resolveCrawlerURL(c.baseURL, rawURL)
	if err != nil {
		return "", "", 0, fmt.Errorf("%w: invalid artifact_url: %v", ErrCrawlerProtocol, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("create crawler artifact request: %w", err)
	}
	request.Header.Set("Accept", "application/jsonl, application/x-ndjson, text/plain")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", "", 0, fmt.Errorf("movie catalog crawler artifact request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", 0, &CrawlerServiceError{StatusCode: response.StatusCode, Message: response.Status}
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("create crawler artifact directory: %w", err)
	}
	file, err := os.CreateTemp(artifactDir, ".eiga_catalog-*.jsonl")
	if err != nil {
		return "", "", 0, fmt.Errorf("create crawler artifact staging file: %w", err)
	}
	path := file.Name()
	removeOnError := true
	defer func() {
		file.Close()
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(file, hash), response.Body, maxCrawlerArtifact+1)
	if err != nil && err != io.EOF {
		return "", "", 0, fmt.Errorf("download crawler artifact: %w", err)
	}
	if written > maxCrawlerArtifact {
		return "", "", 0, fmt.Errorf("%w: artifact exceeds %d bytes", ErrCrawlerProtocol, maxCrawlerArtifact)
	}
	if written == 0 {
		return "", "", 0, fmt.Errorf("%w: artifact is empty", ErrCrawlerProtocol)
	}
	if err := file.Close(); err != nil {
		return "", "", 0, fmt.Errorf("close crawler artifact: %w", err)
	}
	removeOnError = false
	return path, hex.EncodeToString(hash.Sum(nil)), written, nil
}

func resolveCrawlerURL(baseURL string, raw string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return "", fmt.Errorf("invalid crawler base URL")
	}
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if target.IsAbs() && target.Host != base.Host {
		return "", fmt.Errorf("cross-host URL is not allowed")
	}
	return base.ResolveReference(target).String(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
