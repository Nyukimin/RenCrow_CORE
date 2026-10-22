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

// ProjectionHead is the durable accepted state WORLD reports for one
// destination/Actor/kind. epoch_unchanged is only a completion after this head
// has been read back and matched to the exact envelope CORE intended to send.
type ProjectionHead struct {
	ActorID         string
	ActorInstanceID string
	Kind            string
	SourceEpoch     string
	SourceRevision  uint64
	Digest          string
}

// ErrProjectionHeadNotFound means the destination has no accepted head. It is
// terminal for the current outbox record because replaying the same bytes
// cannot create one.
var ErrProjectionHeadNotFound = errors.New("world projection: accepted head not found")

// HeadReadError classifies a failed head read. Only transport, timeout, and
// server-side failures are retryable; a malformed or unauthorized response is
// not.
type HeadReadError struct {
	HTTPStatus int
	Retryable  bool
	Err        error
}

func (e *HeadReadError) Error() string {
	if e == nil {
		return "world projection: head read failed"
	}
	if e.Err != nil {
		return "world projection: head read failed: " + e.Err.Error()
	}
	return fmt.Sprintf("world projection: head read failed with HTTP %d", e.HTTPStatus)
}

func (e *HeadReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Publisher sends one already-built envelope. It never rebuilds or mutates the
// payload; retries must send the same bytes and source revision.
type Publisher interface {
	Publish(context.Context, domainprojection.Target, string, []byte, bool) (PublishResult, error)
}

// HeadReader reads back WORLD's accepted projection head. It is required for
// epoch_unchanged, which acknowledges the current epoch without proving that
// the submitted revision/digest is the accepted head.
type HeadReader interface {
	ReadHead(context.Context, domainprojection.Target, string) (ProjectionHead, error)
}

var _ HeadReader = (*HTTPPublisher)(nil)

// ErrReconcileCredentialNotConfigured means a reconcile publish was requested
// but this publisher holds no separate reconcile credential. Reconcile is a
// distinct capability, so CORE never falls back to the projection credential.
var ErrReconcileCredentialNotConfigured = errors.New("world projection: reconcile credential is not configured")

// HTTPPublisher is the authenticated CORE -> WORLD projection writer.
type HTTPPublisher struct {
	base  *url.URL
	token string
	// reconcileToken authorizes an explicit source epoch switch. It stays empty
	// for a projection-only publisher, which then fails closed on reconcile.
	reconcileToken string
	client         *http.Client
	basePath       string
}

// NewHTTPPublisher validates a base URL and credential. Redirects are not
// followed so a bearer credential cannot be forwarded to another origin.
// The resulting publisher can project but never reconcile.
func NewHTTPPublisher(baseURL, credential string, timeout time.Duration) (*HTTPPublisher, error) {
	return newHTTPPublisher(baseURL, credential, "", timeout)
}

// NewHTTPPublisherWithReconcile additionally authorizes an explicit source
// epoch switch with a separate credential. Reusing the projection credential
// is rejected so one leaked WORLD token cannot both project and re-baseline
// CORE's projection history.
func NewHTTPPublisherWithReconcile(baseURL, credential, reconcileCredential string, timeout time.Duration) (*HTTPPublisher, error) {
	if reconcileCredential == "" {
		return nil, errors.New("world projection: reconcile credential is required")
	}
	return newHTTPPublisher(baseURL, credential, reconcileCredential, timeout)
}

func newHTTPPublisher(baseURL, credential, reconcileCredential string, timeout time.Duration) (*HTTPPublisher, error) {
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
	if reconcileCredential != "" {
		if strings.TrimSpace(reconcileCredential) != reconcileCredential || strings.ContainsAny(reconcileCredential, "\r\n") {
			return nil, errors.New("world projection: reconcile credential is invalid")
		}
		if reconcileCredential == credential {
			return nil, errors.New("world projection: reconcile credential must differ from the projection credential")
		}
	}
	return &HTTPPublisher{
		base:           parsed,
		token:          credential,
		reconcileToken: reconcileCredential,
		basePath:       strings.TrimRight(parsed.Path, "/"),
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
	token := p.token
	if reconcile {
		if p.reconcileToken == "" {
			return PublishResult{}, ErrReconcileCredentialNotConfigured
		}
		token = p.reconcileToken
	}

	u := p.projectionURL(target, kind)
	if reconcile {
		u.RawQuery = "reconcile=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(envelope))
	if err != nil {
		return PublishResult{}, fmt.Errorf("world projection: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

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

func (p *HTTPPublisher) projectionURL(target domainprojection.Target, kind string) *url.URL {
	u := *p.base
	u.Path = p.basePath + "/v1/worlds/" + target.WorldID + "/timelines/" + target.TimelineID +
		"/core-projections/" + target.ActorTransportID + "/" + kind
	u.RawPath = p.basePath + "/v1/worlds/" + url.PathEscape(target.WorldID) + "/timelines/" +
		url.PathEscape(target.TimelineID) + "/core-projections/" + url.PathEscape(target.ActorTransportID) + "/" + kind
	return &u
}

func (p *HTTPPublisher) ReadHead(ctx context.Context, target domainprojection.Target, kind string) (ProjectionHead, error) {
	if p == nil || p.client == nil || p.base == nil {
		return ProjectionHead{}, errors.New("world projection: publisher is not configured")
	}
	if ctx == nil {
		return ProjectionHead{}, errors.New("world projection: context is nil")
	}
	if err := target.Validate(); err != nil {
		return ProjectionHead{}, err
	}
	if kind != domainprojection.KindWorkSet && kind != domainprojection.KindLifePolicy {
		return ProjectionHead{}, errors.New("world projection: unsupported projection kind")
	}
	u := p.projectionURL(target, kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ProjectionHead{}, fmt.Errorf("world projection: build head request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.token)

	resp, err := p.client.Do(req)
	if err != nil {
		return ProjectionHead{}, &HeadReadError{Retryable: true, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAckBodyBytes+1))
	if err != nil {
		return ProjectionHead{}, &HeadReadError{HTTPStatus: resp.StatusCode, Retryable: true, Err: err}
	}
	if len(body) > maxAckBodyBytes {
		return ProjectionHead{}, &HeadReadError{HTTPStatus: resp.StatusCode, Err: errors.New("head response is too large")}
	}
	if resp.StatusCode == http.StatusNotFound {
		return ProjectionHead{}, ErrProjectionHeadNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return ProjectionHead{}, &HeadReadError{
			HTTPStatus: resp.StatusCode,
			Retryable:  retryableStatus(resp.StatusCode),
			Err:        fmt.Errorf("WORLD returned HTTP %d", resp.StatusCode),
		}
	}
	var parsed struct {
		SchemaVersion int `json:"schema_version"`
		Projection    struct {
			ActorID         string `json:"actor_id"`
			ActorInstanceID string `json:"actor_instance_id"`
			ProjectionKind  string `json:"projection_kind"`
			SourceEpoch     string `json:"source_epoch"`
			SourceRevision  uint64 `json:"source_revision"`
			Digest          string `json:"digest"`
		} `json:"projection"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ProjectionHead{}, &HeadReadError{HTTPStatus: resp.StatusCode, Err: errors.New("head response is not valid JSON")}
	}
	if parsed.SchemaVersion != 1 || parsed.Projection.ProjectionKind != kind ||
		parsed.Projection.ActorID == "" || parsed.Projection.ActorInstanceID == "" ||
		parsed.Projection.SourceEpoch == "" || parsed.Projection.SourceRevision == 0 ||
		parsed.Projection.Digest == "" {
		return ProjectionHead{}, &HeadReadError{HTTPStatus: resp.StatusCode, Err: errors.New("head response schema is invalid")}
	}
	return ProjectionHead{
		ActorID:         parsed.Projection.ActorID,
		ActorInstanceID: parsed.Projection.ActorInstanceID,
		Kind:            parsed.Projection.ProjectionKind,
		SourceEpoch:     parsed.Projection.SourceEpoch,
		SourceRevision:  parsed.Projection.SourceRevision,
		Digest:          parsed.Projection.Digest,
	}, nil
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
