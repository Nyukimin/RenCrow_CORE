package personrelatedcatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCollectorTimeout   = 90 * time.Second
	maxCollectorResponse      = 2 << 20
	maxCollectorArtifact      = 64 << 20
	maxCollectorArtifactBytes = maxCollectorArtifact
	collectorEndpointPath     = "/v1/person-related-catalog/collections"
	maxCollectorAttempts      = 3
	defaultRetryDelay         = 100 * time.Millisecond
)

var (
	ErrCollectorUnavailable = errors.New("person related catalog collector unavailable")
	ErrCollectorProtocol    = errors.New("person related catalog collector protocol error")
	ErrArtifactIntegrity    = errors.New("person related catalog artifact integrity failure")
)

const (
	CollectionStatusReady       = "ready"
	CollectionStatusUnavailable = "unavailable"
	CollectionStatusAmbiguous   = "ambiguous"
	CollectionStatusRejected    = "rejected"
)

type CollectionRequest struct {
	MovieCatalogPersonID string
	PersonName           string
	PersonURL            string
	Category             string
	Source               string
	WikidataQID          string
	WikidataCanonicalURL string
	NDLAuthorityURI      string
}

type CollectionResult struct {
	ArtifactURL    string
	ArtifactSHA256 string
	ArtifactBytes  int64
	Artifact       []byte
	Status         string
	ReasonCode     string
	Retryable      bool
	RetryAfter     time.Duration
	RetrievedAt    string
	Source         string
	Candidates     []string
}

type Collector interface {
	Collect(ctx context.Context, request CollectionRequest) (CollectionResult, error)
}

// HTTPCollectorOptions provides deterministic retry seams for tests and keeps
// the production collector bounded to a small, fixed number of attempts.
type HTTPCollectorOptions struct {
	HTTPClient  *http.Client
	Sleep       func(context.Context, time.Duration) error
	Now         func() time.Time
	MaxAttempts int
}

type HTTPCollector struct {
	baseURL     string
	httpClient  *http.Client
	sleep       func(context.Context, time.Duration) error
	now         func() time.Time
	maxAttempts int
}

func NewHTTPCollector(baseURL string, timeout time.Duration) *HTTPCollector {
	return NewHTTPCollectorWithOptions(baseURL, timeout, HTTPCollectorOptions{})
}

func NewHTTPCollectorWithOptions(baseURL string, timeout time.Duration, options HTTPCollectorOptions) *HTTPCollector {
	if timeout <= 0 {
		timeout = defaultCollectorTimeout
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if client.Timeout <= 0 {
		client.Timeout = timeout
	}
	// Keep the same-origin redirect guard even when a caller injects a client.
	client.CheckRedirect = sameOriginRedirect
	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > maxCollectorAttempts {
		maxAttempts = maxCollectorAttempts
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &HTTPCollector{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient:  client,
		sleep:       options.Sleep,
		now:         now,
		maxAttempts: maxAttempts,
	}
}

func sameOriginRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	previous := via[len(via)-1].URL
	if request.URL.Scheme != previous.Scheme || request.URL.Host != previous.Host {
		return fmt.Errorf("redirect leaves provider origin")
	}
	return nil
}

func ResolveCollectionProviderBaseURL() string {
	return strings.TrimSpace(os.Getenv("RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL"))
}

func NewConfiguredCollector(timeout time.Duration) Collector {
	baseURL := ResolveCollectionProviderBaseURL()
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	return NewHTTPCollector(baseURL, timeout)
}

type collectionRequestPayload struct {
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Name                 string `json:"name"`
	URL                  string `json:"url"`
	Category             string `json:"category"`
	Source               string `json:"source,omitempty"`
	WikidataQID          string `json:"wikidata_qid,omitempty"`
	WikidataCanonicalURL string `json:"wikidata_canonical_url,omitempty"`
	NDLAuthorityURI      string `json:"ndl_authority_uri,omitempty"`
}

type collectionResponsePayload struct {
	Status            string   `json:"status"`
	ReasonCode        string   `json:"reason_code"`
	Retryable         bool     `json:"retryable"`
	RetryAfterSeconds int      `json:"retry_after_seconds"`
	RetrievedAt       string   `json:"retrieved_at"`
	Source            string   `json:"source"`
	Candidates        []string `json:"candidates"`
	ArtifactURL       string   `json:"artifact_url"`
	ArtifactSHA256    string   `json:"artifact_sha256"`
	ArtifactBytes     int64    `json:"artifact_bytes"`
}

func (c *HTTPCollector) Collect(ctx context.Context, request CollectionRequest) (CollectionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || strings.TrimSpace(c.baseURL) == "" {
		return CollectionResult{}, fmt.Errorf("%w: set RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL", ErrCollectorUnavailable)
	}
	origin, err := collectorOrigin(c.baseURL)
	if err != nil {
		return CollectionResult{}, fmt.Errorf("%w: invalid provider URL: %v", ErrCollectorProtocol, err)
	}
	if err := validateCollectionRequest(request); err != nil {
		return CollectionResult{}, err
	}
	payload, err := json.Marshal(collectionRequestPayload{
		MovieCatalogPersonID: strings.TrimSpace(request.MovieCatalogPersonID),
		Name:                 strings.TrimSpace(request.PersonName),
		URL:                  strings.TrimSpace(request.PersonURL),
		Category:             request.Category,
		Source:               strings.TrimSpace(request.Source),
		WikidataQID:          strings.TrimSpace(request.WikidataQID),
		WikidataCanonicalURL: strings.TrimSpace(request.WikidataCanonicalURL),
		NDLAuthorityURI:      strings.TrimSpace(request.NDLAuthorityURI),
	})
	if err != nil {
		return CollectionResult{}, fmt.Errorf("%w: encode collection request: %v", ErrCollectorProtocol, err)
	}
	result, err := c.post(ctx, origin, payload)
	if err != nil {
		return CollectionResult{}, err
	}
	status := normalizeCollectionStatus(result.Status)
	if status == "" {
		// The original provider contract omitted status; preserve that wire
		// compatibility while exposing the normalized status to callers.
		status = CollectionStatusReady
	}
	if !validCollectionStatus(status) {
		return CollectionResult{}, fmt.Errorf("%w: status %q is invalid", ErrCollectorProtocol, result.Status)
	}
	if result.RetrievedAt == "" {
		result.RetrievedAt = c.now().UTC().Format(time.RFC3339)
	}
	if result.Source == "" {
		result.Source = strings.TrimSpace(request.Source)
	}
	semantic := CollectionResult{
		Status:      status,
		ReasonCode:  strings.TrimSpace(result.ReasonCode),
		Retryable:   result.Retryable,
		RetryAfter:  time.Duration(result.RetryAfterSeconds) * time.Second,
		RetrievedAt: strings.TrimSpace(result.RetrievedAt),
		Source:      strings.TrimSpace(result.Source),
		Candidates:  append([]string(nil), result.Candidates...),
	}
	if status != CollectionStatusReady {
		return semantic, nil
	}
	if strings.TrimSpace(result.ArtifactURL) == "" {
		return CollectionResult{}, fmt.Errorf("%w: artifact_url is missing", ErrCollectorProtocol)
	}
	if result.ArtifactBytes <= 0 || result.ArtifactBytes > maxCollectorArtifact {
		return CollectionResult{}, fmt.Errorf("%w: artifact_bytes must be between 1 and %d", ErrCollectorProtocol, maxCollectorArtifact)
	}
	expectedHash := strings.ToLower(strings.TrimSpace(result.ArtifactSHA256))
	if len(expectedHash) != sha256.Size*2 {
		return CollectionResult{}, fmt.Errorf("%w: artifact_sha256 must be a 64-character hex digest", ErrCollectorProtocol)
	}
	if _, err := hex.DecodeString(expectedHash); err != nil {
		return CollectionResult{}, fmt.Errorf("%w: artifact_sha256 is invalid: %v", ErrCollectorProtocol, err)
	}
	artifactURL, err := resolveCollectorURL(origin, result.ArtifactURL)
	if err != nil {
		return CollectionResult{}, fmt.Errorf("%w: invalid artifact_url: %v", ErrCollectorProtocol, err)
	}
	artifact, err := c.download(ctx, artifactURL, expectedHash, result.ArtifactBytes)
	if err != nil {
		return CollectionResult{}, err
	}
	actualHash := sha256.Sum256(artifact)
	actualHashHex := hex.EncodeToString(actualHash[:])
	if actualHashHex != expectedHash || int64(len(artifact)) != result.ArtifactBytes {
		return CollectionResult{}, fmt.Errorf("%w: expected sha256=%s bytes=%d actual sha256=%s bytes=%d", ErrArtifactIntegrity, expectedHash, result.ArtifactBytes, actualHashHex, len(artifact))
	}
	semantic.ArtifactURL = artifactURL
	semantic.ArtifactSHA256 = actualHashHex
	semantic.ArtifactBytes = int64(len(artifact))
	semantic.Artifact = artifact
	return semantic, nil
}

func validateCollectionRequest(request CollectionRequest) error {
	if strings.TrimSpace(request.MovieCatalogPersonID) == "" || strings.TrimSpace(request.PersonName) == "" || strings.TrimSpace(request.PersonURL) == "" {
		return fmt.Errorf("%w: person identity fields are required", ErrCollectorProtocol)
	}
	if !validCollectorCategory(request.Category) {
		return fmt.Errorf("%w: category %q is invalid", ErrCollectorProtocol, request.Category)
	}
	if source := strings.TrimSpace(request.Source); source != "" && !contractFreeSourceAllowed(request.Category, source) {
		return fmt.Errorf("%w: source %q is not approved for category %q", ErrCollectorProtocol, source, request.Category)
	}
	return nil
}

func validCollectorCategory(category string) bool {
	switch category {
	case CategoryDrama, CategoryAward, CategoryMusic, CategoryAnime, CategoryNovel, CategoryManga:
		return true
	default:
		return false
	}
}

func collectorOrigin(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("provider URL must be an http(s) origin")
	}
	return &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}, nil
}

func resolveCollectorURL(origin *url.URL, raw string) (string, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.User != nil {
		return "", fmt.Errorf("artifact URL is invalid")
	}
	resolved := origin.ResolveReference(target)
	if resolved.Scheme != origin.Scheme || resolved.Host != origin.Host {
		return "", fmt.Errorf("artifact URL must use the provider origin")
	}
	return resolved.String(), nil
}

func (c *HTTPCollector) post(ctx context.Context, origin *url.URL, payload []byte) (collectionResponsePayload, error) {
	endpoint := origin.ResolveReference(&url.URL{Path: collectorEndpointPath}).String()
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		result, retryAfter, retryable, err := c.postOnce(ctx, endpoint, payload)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retryable || attempt == c.maxAttempts {
			break
		}
		if err := c.wait(ctx, retryAfterOrBackoff(retryAfter, attempt)); err != nil {
			return collectionResponsePayload{}, err
		}
	}
	return collectionResponsePayload{}, lastErr
}

func (c *HTTPCollector) postOnce(ctx context.Context, endpoint string, payload []byte) (collectionResponsePayload, time.Duration, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return collectionResponsePayload{}, 0, false, fmt.Errorf("create collection request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return collectionResponsePayload{}, 0, temporaryNetworkError(err), fmt.Errorf("collection provider request failed: %w", err)
	}
	defer response.Body.Close()
	payloadBytes, err := io.ReadAll(io.LimitReader(response.Body, maxCollectorResponse+1))
	if err != nil {
		return collectionResponsePayload{}, 0, temporaryNetworkError(err), fmt.Errorf("read collection provider response: %w", err)
	}
	if len(payloadBytes) > maxCollectorResponse {
		return collectionResponsePayload{}, 0, false, fmt.Errorf("%w: response exceeds %d bytes", ErrCollectorProtocol, maxCollectorResponse)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return collectionResponsePayload{}, parseRetryAfter(response.Header.Get("Retry-After"), c.now()), retryable, fmt.Errorf("%w: provider status %s", ErrCollectorProtocol, response.Status)
	}
	var result collectionResponsePayload
	if err := json.Unmarshal(payloadBytes, &result); err != nil {
		return collectionResponsePayload{}, 0, false, fmt.Errorf("%w: decode provider response: %v", ErrCollectorProtocol, err)
	}
	return result, 0, false, nil
}

func (c *HTTPCollector) download(ctx context.Context, artifactURL, expectedHash string, expectedBytes int64) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		artifact, retryAfter, retryable, err := c.downloadOnce(ctx, artifactURL, expectedHash, expectedBytes)
		if err == nil {
			return artifact, nil
		}
		lastErr = err
		if !retryable || attempt == c.maxAttempts {
			break
		}
		if err := c.wait(ctx, retryAfterOrBackoff(retryAfter, attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *HTTPCollector) downloadOnce(ctx context.Context, artifactURL, expectedHash string, expectedBytes int64) ([]byte, time.Duration, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL, nil)
	if err != nil {
		return nil, 0, false, fmt.Errorf("create artifact request: %w", err)
	}
	request.Header.Set("Accept", "application/jsonl, application/x-ndjson, text/plain")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, 0, temporaryNetworkError(err), fmt.Errorf("artifact request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retryable := response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		return nil, parseRetryAfter(response.Header.Get("Retry-After"), c.now()), retryable, fmt.Errorf("%w: artifact status %s", ErrCollectorProtocol, response.Status)
	}
	if contentLength := strings.TrimSpace(response.Header.Get("Content-Length")); contentLength != "" {
		length, parseErr := strconv.ParseInt(contentLength, 10, 64)
		if parseErr != nil || length < 0 {
			return nil, 0, false, fmt.Errorf("%w: artifact Content-Length is invalid", ErrCollectorProtocol)
		}
		if length > maxCollectorArtifact {
			return nil, 0, false, fmt.Errorf("%w: artifact exceeds %d bytes", ErrCollectorProtocol, maxCollectorArtifact)
		}
	}
	artifact, err := io.ReadAll(io.LimitReader(response.Body, maxCollectorArtifact+1))
	if err != nil {
		return nil, 0, temporaryNetworkError(err), fmt.Errorf("download artifact: %w", err)
	}
	if len(artifact) == 0 || len(artifact) > maxCollectorArtifact {
		return nil, 0, false, fmt.Errorf("%w: artifact size is outside bounds", ErrCollectorProtocol)
	}
	actualHash := sha256.Sum256(artifact)
	actualHashHex := hex.EncodeToString(actualHash[:])
	if actualHashHex != expectedHash || int64(len(artifact)) != expectedBytes {
		return nil, 0, false, fmt.Errorf("%w: expected sha256=%s bytes=%d actual sha256=%s bytes=%d", ErrArtifactIntegrity, expectedHash, expectedBytes, actualHashHex, len(artifact))
	}
	return artifact, 0, false, nil
}

func normalizeCollectionStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "ok" {
		return CollectionStatusReady
	}
	return status
}

func validCollectionStatus(status string) bool {
	switch status {
	case CollectionStatusReady, CollectionStatusUnavailable, CollectionStatusAmbiguous, CollectionStatusRejected:
		return true
	default:
		return false
	}
}

func temporaryNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := when.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

func retryAfterOrBackoff(retryAfter time.Duration, attempt int) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	delay := defaultRetryDelay
	for i := 1; i < attempt; i++ {
		delay *= 2
	}
	return delay
}

func (c *HTTPCollector) wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	if c.sleep != nil {
		return c.sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
