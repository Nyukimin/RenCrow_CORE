package personrelatedcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const identityResolverEndpointPath = "/v1/person-related-catalog/identities/resolve"

// IdentityResolveRequest is the only request accepted by the fixed provider
// transport. It contains an immutable catalog anchor and already confirmed
// IDs; it has no free-form query, URL, or arbitrary provider selector.
type IdentityResolveRequest struct {
	RunID                string            `json:"run_id"`
	MovieCatalogPersonID string            `json:"movie_catalog_person_id"`
	PersonName           string            `json:"name"`
	PublicPersonURL      string            `json:"url"`
	ConfirmedExternalIDs map[string]string `json:"confirmed_external_ids,omitempty"`
}

type IdentityResolveResult struct {
	Status      IdentityStatus
	ReasonCode  string
	Retryable   bool
	RetryAfter  time.Duration
	RetrievedAt string
	ExpiresAt   string
	Candidates  []IdentityEvidence
}

type IdentityResolver interface {
	ResolveIdentity(context.Context, IdentityResolveRequest) (IdentityResolveResult, error)
}

type IdentityResolveError struct {
	Retryable  bool
	RetryAfter time.Duration
	Err        error
}

func (e *IdentityResolveError) Error() string {
	if e == nil || e.Err == nil {
		return "identity resolver request failed"
	}
	return e.Err.Error()
}

func (e *IdentityResolveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type HTTPIdentityResolver struct{ transport *HTTPCollector }

func NewHTTPIdentityResolver(baseURL string, timeout time.Duration) *HTTPIdentityResolver {
	return &HTTPIdentityResolver{transport: NewHTTPCollector(baseURL, timeout)}
}

type identityResolveWireResponse struct {
	Status            string             `json:"status"`
	ReasonCode        string             `json:"reason_code"`
	Retryable         bool               `json:"retryable"`
	RetryAfterSeconds int                `json:"retry_after_seconds"`
	RetrievedAt       string             `json:"retrieved_at"`
	ExpiresAt         string             `json:"expires_at"`
	Candidates        []IdentityEvidence `json:"candidates"`
}

func (r *HTTPIdentityResolver) ResolveIdentity(ctx context.Context, request IdentityResolveRequest) (IdentityResolveResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.transport == nil || strings.TrimSpace(r.transport.baseURL) == "" {
		return IdentityResolveResult{}, fmt.Errorf("%w: identity provider is not configured", ErrCollectorUnavailable)
	}
	request, err := normalizeIdentityResolveRequest(request)
	if err != nil {
		return IdentityResolveResult{}, err
	}
	origin, err := collectorOrigin(r.transport.baseURL)
	if err != nil {
		return IdentityResolveResult{}, fmt.Errorf("%w: invalid identity provider URL: %v", ErrCollectorProtocol, err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return IdentityResolveResult{}, fmt.Errorf("%w: encode identity request: %v", ErrCollectorProtocol, err)
	}
	endpoint := origin.ResolveReference(&url.URL{Path: identityResolverEndpointPath}).String()
	var lastErr error
	for attempt := 1; attempt <= r.transport.maxAttempts; attempt++ {
		wire, retryAfter, retryable, requestErr := r.postIdentityOnce(ctx, endpoint, payload)
		if requestErr == nil {
			status := IdentityStatus(strings.ToLower(strings.TrimSpace(wire.Status)))
			if status != IdentityStatusConfirmed && status != IdentityStatusAmbiguous && status != IdentityStatusUnresolved {
				return IdentityResolveResult{}, fmt.Errorf("%w: identity status %q is invalid", ErrCollectorProtocol, wire.Status)
			}
			return IdentityResolveResult{Status: status, ReasonCode: strings.TrimSpace(wire.ReasonCode), Retryable: wire.Retryable, RetryAfter: time.Duration(wire.RetryAfterSeconds) * time.Second, RetrievedAt: strings.TrimSpace(wire.RetrievedAt), ExpiresAt: strings.TrimSpace(wire.ExpiresAt), Candidates: wire.Candidates}, nil
		}
		lastErr = requestErr
		if !retryable || attempt == r.transport.maxAttempts {
			break
		}
		if waitErr := r.transport.wait(ctx, retryAfterOrBackoff(retryAfter, attempt)); waitErr != nil {
			return IdentityResolveResult{}, waitErr
		}
	}
	return IdentityResolveResult{}, lastErr
}

func normalizeIdentityResolveRequest(request IdentityResolveRequest) (IdentityResolveRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.MovieCatalogPersonID = strings.TrimSpace(request.MovieCatalogPersonID)
	request.PersonName = strings.TrimSpace(request.PersonName)
	request.PublicPersonURL = strings.TrimSpace(request.PublicPersonURL)
	if request.RunID == "" || request.MovieCatalogPersonID == "" || request.PersonName == "" {
		return IdentityResolveRequest{}, fmt.Errorf("%w: identity request anchor fields are required", ErrCollectorProtocol)
	}
	if request.PublicPersonURL != "" && !validHTTPURL(request.PublicPersonURL) {
		return IdentityResolveRequest{}, fmt.Errorf("%w: identity request person URL is invalid", ErrCollectorProtocol)
	}
	if len(request.ConfirmedExternalIDs) > 20 {
		return IdentityResolveRequest{}, fmt.Errorf("%w: identity request has too many confirmed IDs", ErrCollectorProtocol)
	}
	if len(request.ConfirmedExternalIDs) > 0 {
		normalized := make(map[string]string, len(request.ConfirmedExternalIDs))
		for authority, externalID := range request.ConfirmedExternalIDs {
			authority = normalizeIdentityAuthority(authority)
			externalID = strings.TrimSpace(externalID)
			if authority == "" || externalID == "" {
				return IdentityResolveRequest{}, fmt.Errorf("%w: identity request confirmed ID is invalid", ErrCollectorProtocol)
			}
			normalized[authority] = externalID
		}
		request.ConfirmedExternalIDs = normalized
	}
	return request, nil
}

func (r *HTTPIdentityResolver) postIdentityOnce(ctx context.Context, endpoint string, payload []byte) (identityResolveWireResponse, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return identityResolveWireResponse{}, 0, false, &IdentityResolveError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.transport.httpClient.Do(req)
	if err != nil {
		return identityResolveWireResponse{}, 0, temporaryNetworkError(err), &IdentityResolveError{Retryable: temporaryNetworkError(err), Err: fmt.Errorf("identity provider request failed: %w", err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCollectorResponse+1))
	if err != nil {
		return identityResolveWireResponse{}, 0, temporaryNetworkError(err), &IdentityResolveError{Retryable: temporaryNetworkError(err), Err: fmt.Errorf("read identity provider response: %w", err)}
	}
	if len(body) > maxCollectorResponse {
		return identityResolveWireResponse{}, 0, false, &IdentityResolveError{Err: fmt.Errorf("%w: identity response exceeds limit", ErrCollectorProtocol)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return identityResolveWireResponse{}, parseRetryAfter(resp.Header.Get("Retry-After"), r.transport.now()), retryable, &IdentityResolveError{Retryable: retryable, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), r.transport.now()), Err: fmt.Errorf("%w: identity provider status %s", ErrCollectorProtocol, resp.Status)}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire identityResolveWireResponse
	if err := decoder.Decode(&wire); err != nil {
		return identityResolveWireResponse{}, 0, false, &IdentityResolveError{Err: fmt.Errorf("%w: decode identity provider response: %v", ErrCollectorProtocol, err)}
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return identityResolveWireResponse{}, 0, false, &IdentityResolveError{Err: fmt.Errorf("%w: identity response has trailing data", ErrCollectorProtocol)}
	}
	return wire, 0, false, nil
}

var _ IdentityResolver = (*HTTPIdentityResolver)(nil)
