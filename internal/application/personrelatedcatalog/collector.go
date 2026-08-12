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
)

var (
	ErrCollectorUnavailable = errors.New("person related catalog collector unavailable")
	ErrCollectorProtocol    = errors.New("person related catalog collector protocol error")
	ErrArtifactIntegrity    = errors.New("person related catalog artifact integrity failure")
)

type CollectionRequest struct {
	MovieCatalogPersonID string
	PersonName           string
	PersonURL            string
	Category             string
}

type CollectionResult struct {
	ArtifactURL    string
	ArtifactSHA256 string
	ArtifactBytes  int64
	Artifact       []byte
}

type Collector interface {
	Collect(ctx context.Context, request CollectionRequest) (CollectionResult, error)
}

type HTTPCollector struct {
	baseURL    string
	httpClient *http.Client
}

func NewHTTPCollector(baseURL string, timeout time.Duration) *HTTPCollector {
	if timeout <= 0 {
		timeout = defaultCollectorTimeout
	}
	return &HTTPCollector{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) == 0 {
					return nil
				}
				previous := via[len(via)-1].URL
				if request.URL.Scheme != previous.Scheme || request.URL.Host != previous.Host {
					return fmt.Errorf("redirect leaves provider origin")
				}
				return nil
			},
		},
	}
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
}

type collectionResponsePayload struct {
	ArtifactURL    string `json:"artifact_url"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ArtifactBytes  int64  `json:"artifact_bytes"`
}

func (c *HTTPCollector) Collect(ctx context.Context, request CollectionRequest) (CollectionResult, error) {
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
	})
	if err != nil {
		return CollectionResult{}, fmt.Errorf("%w: encode collection request: %v", ErrCollectorProtocol, err)
	}
	result, err := c.post(ctx, origin, payload)
	if err != nil {
		return CollectionResult{}, err
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
	return CollectionResult{ArtifactURL: artifactURL, ArtifactSHA256: actualHashHex, ArtifactBytes: int64(len(artifact)), Artifact: artifact}, nil
}

func validateCollectionRequest(request CollectionRequest) error {
	if strings.TrimSpace(request.MovieCatalogPersonID) == "" || strings.TrimSpace(request.PersonName) == "" || strings.TrimSpace(request.PersonURL) == "" {
		return fmt.Errorf("%w: person identity fields are required", ErrCollectorProtocol)
	}
	if !validCollectorCategory(request.Category) {
		return fmt.Errorf("%w: category %q is invalid", ErrCollectorProtocol, request.Category)
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return collectionResponsePayload{}, fmt.Errorf("create collection request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return collectionResponsePayload{}, fmt.Errorf("collection provider request failed: %w", err)
	}
	defer response.Body.Close()
	payloadBytes, err := io.ReadAll(io.LimitReader(response.Body, maxCollectorResponse+1))
	if err != nil {
		return collectionResponsePayload{}, fmt.Errorf("read collection provider response: %w", err)
	}
	if len(payloadBytes) > maxCollectorResponse {
		return collectionResponsePayload{}, fmt.Errorf("%w: response exceeds %d bytes", ErrCollectorProtocol, maxCollectorResponse)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return collectionResponsePayload{}, fmt.Errorf("%w: provider status %s", ErrCollectorProtocol, response.Status)
	}
	var result collectionResponsePayload
	if err := json.Unmarshal(payloadBytes, &result); err != nil {
		return collectionResponsePayload{}, fmt.Errorf("%w: decode provider response: %v", ErrCollectorProtocol, err)
	}
	return result, nil
}

func (c *HTTPCollector) download(ctx context.Context, artifactURL, expectedHash string, expectedBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create artifact request: %w", err)
	}
	request.Header.Set("Accept", "application/jsonl, application/x-ndjson, text/plain")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("artifact request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: artifact status %s", ErrCollectorProtocol, response.Status)
	}
	if contentLength := strings.TrimSpace(response.Header.Get("Content-Length")); contentLength != "" {
		length, parseErr := strconv.ParseInt(contentLength, 10, 64)
		if parseErr != nil || length < 0 {
			return nil, fmt.Errorf("%w: artifact Content-Length is invalid", ErrCollectorProtocol)
		}
		if length > maxCollectorArtifact {
			return nil, fmt.Errorf("%w: artifact exceeds %d bytes", ErrCollectorProtocol, maxCollectorArtifact)
		}
	}
	artifact, err := io.ReadAll(io.LimitReader(response.Body, maxCollectorArtifact+1))
	if err != nil {
		return nil, fmt.Errorf("download artifact: %w", err)
	}
	if len(artifact) == 0 || len(artifact) > maxCollectorArtifact {
		return nil, fmt.Errorf("%w: artifact size is outside bounds", ErrCollectorProtocol)
	}
	actualHash := sha256.Sum256(artifact)
	actualHashHex := hex.EncodeToString(actualHash[:])
	if actualHashHex != expectedHash || int64(len(artifact)) != expectedBytes {
		return nil, fmt.Errorf("%w: expected sha256=%s bytes=%d actual sha256=%s bytes=%d", ErrArtifactIntegrity, expectedHash, expectedBytes, actualHashHex, len(artifact))
	}
	return artifact, nil
}
