package worldprojection

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

const (
	DefaultPublisherTimeout = 5 * time.Second
	maxAckBodyBytes         = 64 * 1024
)

// PublishResult is the classified projection acknowledgement. Retryable is
// true only when replaying the same exact envelope can plausibly succeed.
// Conflicts and reconciliation gates are terminal for the current record.
type PublishResult struct {
	HTTPStatus        int
	Status            string
	Reason            string
	Retryable         bool
	RequiresReconcile bool
	AcceptedAt        time.Time
}

func (r PublishResult) Successful() bool {
	switch r.Status {
	case "accepted", "duplicate", "epoch_unchanged":
		return true
	default:
		return false
	}
}

// Publisher sends one already-built envelope. It never rebuilds or mutates the
// payload; retries must send the same bytes and source revision.
type Publisher interface {
	Publish(context.Context, domainprojection.Target, string, []byte, bool) (PublishResult, error)
}

// HTTPPublisher is the authenticated CORE -> WORLD projection writer.
type HTTPPublisher struct {
	base     *url.URL
	token    string
	client   *http.Client
	basePath string
}

// NewHTTPPublisher validates a base URL and credential. Redirects are not
// followed so a bearer credential cannot be forwarded to another origin.
func NewHTTPPublisher(baseURL, credential string, timeout time.Duration) (*HTTPPublisher, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, errors.New("world projection: base URL is required")
	}
	if timeout <= 0 {
		return nil, errors.New("world projection: HTTP timeout must be positive")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("world projection: base URL must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	if credential == "" || strings.TrimSpace(credential) != credential || strings.ContainsAny(credential, "\r\n") {
		return nil, errors.New("world projection: credential is invalid")
	}
	return &HTTPPublisher{
		base:     parsed,
		token:    credential,
		basePath: strings.TrimRight(parsed.Path, "/"),
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (p *HTTPPublisher) Publish(ctx context.Context, target domainprojection.Target, kind string, envelope []byte, reconcile bool) (PublishResult, error) {
	if p == nil || p.client == nil || p.base == nil {
		return PublishResult{}, errors.New("world projection: publisher is not configured")
	}
	if ctx == nil {
		return PublishResult{}, errors.New("world projection: context is nil")
	}
	if err := target.Validate(); err != nil {
		return PublishResult{}, err
	}
	if kind != domainprojection.KindWorkSet && kind != domainprojection.KindLifePolicy {
		return PublishResult{}, errors.New("world projection: unsupported projection kind")
	}
	if len(envelope) == 0 || len(envelope) > domainprojection.MaxEnvelopeBytes {
		return PublishResult{}, errors.New("world projection: envelope size is invalid")
	}

	u := *p.base
	u.Path = p.basePath + "/v1/worlds/" + target.WorldID + "/timelines/" + target.TimelineID +
		"/core-projections/" + target.ActorTransportID + "/" + kind
	u.RawPath = p.basePath + "/v1/worlds/" + url.PathEscape(target.WorldID) + "/timelines/" +
		url.PathEscape(target.TimelineID) + "/core-projections/" + url.PathEscape(target.ActorTransportID) + "/" + kind
	if reconcile {
		u.RawQuery = "reconcile=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(envelope))
	if err != nil {
		return PublishResult{}, fmt.Errorf("world projection: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return PublishResult{Retryable: true}, fmt.Errorf("world projection: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAckBodyBytes+1))
	if err != nil {
		return PublishResult{HTTPStatus: resp.StatusCode, Retryable: true}, fmt.Errorf("world projection: read acknowledgement: %w", err)
	}
	if len(body) > maxAckBodyBytes {
		return PublishResult{HTTPStatus: resp.StatusCode, Retryable: true}, errors.New("world projection: acknowledgement is too large")
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusConflict {
		result, parseErr := parseAck(resp.StatusCode, body)
		if parseErr != nil {
			result.Retryable = true
			return result, parseErr
		}
		return result, nil
	}
	result := PublishResult{
		HTTPStatus: resp.StatusCode,
		Retryable:  retryableStatus(resp.StatusCode),
	}
	return result, fmt.Errorf("world projection: WORLD returned HTTP %d", resp.StatusCode)
}

func parseAck(status int, raw []byte) (PublishResult, error) {
	var body struct {
		SchemaVersion int `json:"schema_version"`
		Projection    struct {
			Status     string    `json:"status"`
			Reason     string    `json:"reason"`
			AcceptedAt time.Time `json:"accepted_at"`
		} `json:"projection"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return PublishResult{HTTPStatus: status}, errors.New("world projection: acknowledgement is not valid JSON")
	}
	if body.SchemaVersion != 1 || body.Projection.Status == "" {
		return PublishResult{HTTPStatus: status}, errors.New("world projection: acknowledgement schema is invalid")
	}
	result := PublishResult{
		HTTPStatus: status,
		Status:     body.Projection.Status,
		Reason:     body.Projection.Reason,
		AcceptedAt: body.Projection.AcceptedAt,
	}
	switch result.Status {
	case "accepted", "duplicate", "epoch_unchanged":
		return result, nil
	case "stale_revision", "revision_conflict":
		return result, nil
	case "epoch_superseded", "epoch_requires_reconcile":
		result.RequiresReconcile = true
		return result, nil
	default:
		return result, fmt.Errorf("world projection: unknown acknowledgement status %q", result.Status)
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= 500
}
