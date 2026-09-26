package llmfit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

const (
	defaultHTTPTimeout   = 5 * time.Second
	errorBodyExcerptSize = 2048
)

// HTTPProvider talks to a remote llmfit HTTP server:
//
//	GET {base}/health
//	GET {base}/api/v1/system
//	GET {base}/api/v1/models/top
//	GET {base}/api/v1/models
type HTTPProvider struct {
	nodeID  string
	baseURL string
	client  *http.Client
	// Now stamps CollectedAt on returned observations. Injectable for tests.
	Now func() time.Time
}

// NewHTTPProvider creates a provider for nodeID served at baseURL. A non-positive
// timeout falls back to 5s so no request can hang indefinitely.
func NewHTTPProvider(nodeID, baseURL string, timeout time.Duration) *HTTPProvider {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &HTTPProvider{
		nodeID:  strings.TrimSpace(nodeID),
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: timeout},
		Now:     time.Now,
	}
}

// Health implements CapabilityProvider.
func (p *HTTPProvider) Health(ctx context.Context) error {
	const op = "health"
	body, err := p.get(ctx, op, "/health", nil)
	if err != nil {
		return err
	}
	defer body.Close()
	resp, err := decodeHealth(body)
	if err != nil {
		return newProviderError(ErrorKindInvalidResponse, p.nodeID, op, "invalid health JSON: "+err.Error(), err)
	}
	if status := strings.ToLower(strings.TrimSpace(resp.Status)); status != "ok" {
		return newProviderError(ErrorKindServerError, p.nodeID, op, fmt.Sprintf("llmfit reports status %q", resp.Status), nil)
	}
	return nil
}

// System implements CapabilityProvider.
func (p *HTTPProvider) System(ctx context.Context) (*domainllmops.NodeHardwareProfile, error) {
	const op = "system"
	body, err := p.get(ctx, op, "/api/v1/system", nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	resp, err := decodeSystem(body)
	if err != nil {
		return nil, newProviderError(ErrorKindInvalidResponse, p.nodeID, op, "invalid system JSON: "+err.Error(), err)
	}
	profile := mapSystem(p.nodeID, resp, p.now())
	return &profile, nil
}

// TopModels implements CapabilityProvider.
func (p *HTTPProvider) TopModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	return p.models(ctx, "models/top", "/api/v1/models/top", query, false)
}

// SearchModels implements CapabilityProvider.
func (p *HTTPProvider) SearchModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error) {
	return p.models(ctx, "models", "/api/v1/models", query, true)
}

func (p *HTTPProvider) models(ctx context.Context, op, path string, query ModelFitQuery, withKeyword bool) ([]domainllmops.ModelFitAssessment, error) {
	q, err := validateQuery(p.nodeID, op, query)
	if err != nil {
		return nil, err
	}
	body, err := p.get(ctx, op, path, buildQueryValues(q, withKeyword))
	if err != nil {
		return nil, err
	}
	defer body.Close()
	resp, err := decodeModels(body)
	if err != nil {
		return nil, newProviderError(ErrorKindInvalidResponse, p.nodeID, op, "invalid models JSON: "+err.Error(), err)
	}
	models, skipped := mapModels(p.nodeID, resp, p.now())
	if skipped > 0 {
		log.Printf("[llmfit] node=%s op=%s skipped %d model entries without a name", p.nodeID, op, skipped)
	}
	return models, nil
}

func buildQueryValues(q ModelFitQuery, withKeyword bool) url.Values {
	values := url.Values{}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.MinFit != "" {
		values.Set("min_fit", q.MinFit)
	}
	if q.UseCase != "" {
		values.Set("use_case", q.UseCase)
	}
	if q.Runtime != "" {
		values.Set("runtime", q.Runtime)
	}
	if q.MaxContext > 0 {
		values.Set("max_context", strconv.Itoa(q.MaxContext))
	}
	if q.Sort != "" {
		values.Set("sort", q.Sort)
	}
	if q.IncludeTooTight {
		values.Set("include_too_tight", "true")
	}
	if withKeyword && q.Keyword != "" {
		values.Set("search", q.Keyword)
	}
	return values
}

// get performs the GET and returns the body for 2xx responses. Non-2xx bodies
// are excerpted with a LimitReader and turned into a ProviderError.
func (p *HTTPProvider) get(ctx context.Context, op, path string, values url.Values) (io.ReadCloser, error) {
	if p.baseURL == "" {
		return nil, newProviderError(ErrorKindConfiguration, p.nodeID, op, "endpoint is empty", nil)
	}
	target := p.baseURL + path
	if len(values) > 0 {
		target += "?" + values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, newProviderError(ErrorKindConfiguration, p.nodeID, op, "request creation failed: "+err.Error(), err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, newProviderError(ErrorKindUnreachable, p.nodeID, op, summarizeTransportError(err), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyExcerptSize))
		_ = resp.Body.Close()
		kind := ErrorKindServerError
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			kind = ErrorKindBadRequest
		}
		message := fmt.Sprintf("unexpected status %d", resp.StatusCode)
		if detail := decodeErrorMessage(excerpt); detail != "" {
			message += ": " + detail
		} else if text := strings.TrimSpace(string(excerpt)); text != "" {
			message += ": " + truncate(text, 200)
		}
		return nil, newProviderError(kind, p.nodeID, op, message, nil)
	}
	return resp.Body, nil
}

func (p *HTTPProvider) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

// summarizeTransportError keeps the message short and free of request URLs
// (which would repeat the endpoint in every viewer error field).
func summarizeTransportError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "connection failed: " + urlErr.Err.Error()
	}
	return "connection failed: " + err.Error()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
