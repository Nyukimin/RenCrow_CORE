package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	musicGatewayRequestTimeout = 8 * time.Second
	maxMusicGatewayResponse    = 128 << 10
)

// MusicGatewayClientConfig binds one local Music endpoint, operator actor, and
// bearer credential. HTTPClient exists for isolated contract tests only.
type MusicGatewayClientConfig struct {
	Endpoint    string
	ActorID     string
	BearerToken string
	HTTPClient  *http.Client
}

// MusicGatewayStatusError carries only a status code; response bodies and
// credentials never enter error text or Tool output.
type MusicGatewayStatusError struct {
	StatusCode int
}

func (e *MusicGatewayStatusError) Error() string {
	return fmt.Sprintf("music gateway returned HTTP %d", e.StatusCode)
}

// MusicProjectGatewayClient is a fixed-actor client for the project-storage
// portion of RenCrow_Music's S0 local HTTP API.
type MusicProjectGatewayClient struct {
	endpoint *url.URL
	actorID  string
	token    string
	client   *http.Client
}

var _ MusicProjectGateway = (*MusicProjectGatewayClient)(nil)

func NewMusicProjectGatewayClient(cfg MusicGatewayClientConfig) (*MusicProjectGatewayClient, error) {
	endpoint, err := parseMusicGatewayEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	actorID := strings.TrimSpace(cfg.ActorID)
	if actorID == "" || len(actorID) > 200 || strings.ContainsAny(actorID, "\r\n\x00") {
		return nil, errors.New("music actor_id is invalid")
	}
	token := cfg.BearerToken
	if len(token) < 32 || len(token) > 4096 || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("music bearer token is invalid")
	}

	client := cfg.HTTPClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	} else {
		cloned := *client
		client = &cloned
	}
	client.Timeout = musicGatewayRequestTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &MusicProjectGatewayClient{endpoint: endpoint, actorID: actorID, token: token, client: client}, nil
}

func parseMusicGatewayEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("music endpoint must be an absolute numeric-loopback HTTP origin")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("music endpoint must use a numeric loopback address")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("music endpoint must include a valid port")
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	return endpoint, nil
}

func (c *MusicProjectGatewayClient) ActorID() string { return c.actorID }

func (c *MusicProjectGatewayClient) ProbeCapabilities(ctx context.Context) (MusicCapabilities, error) {
	var capabilities MusicCapabilities
	if err := c.doJSON(ctx, http.MethodGet, "/v1/music/capabilities", nil, "", http.StatusOK, true, &capabilities); err != nil {
		return MusicCapabilities{}, err
	}
	if capabilities.Capabilities == nil || capabilities.ActorID != c.actorID {
		return MusicCapabilities{}, errors.New("music capabilities response does not confirm the configured actor")
	}
	return capabilities, nil
}

func (c *MusicProjectGatewayClient) CreateProject(ctx context.Context, input MusicProjectCreateInput, idempotencyKey string) (MusicProject, error) {
	if len(idempotencyKey) == 0 || len(idempotencyKey) > 200 || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		return MusicProject{}, errors.New("music idempotency key is invalid")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return MusicProject{}, fmt.Errorf("encode music project request: %w", err)
	}
	var project MusicProject
	if err := c.doJSON(ctx, http.MethodPost, "/v1/music/projects", body, idempotencyKey, http.StatusCreated, false, &project); err != nil {
		return MusicProject{}, err
	}
	if err := validateMusicProject(project); err != nil {
		return MusicProject{}, err
	}
	return project, nil
}

func (c *MusicProjectGatewayClient) GetProject(ctx context.Context, projectID string) (MusicProject, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicProject{}, err
	}
	var project MusicProject
	path := "/v1/music/projects/" + url.PathEscape(projectID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, false, &project); err != nil {
		return MusicProject{}, err
	}
	if err := validateMusicProject(project); err != nil {
		return MusicProject{}, err
	}
	if project.ID != projectID {
		return MusicProject{}, errors.New("music project response identity does not match the requested project")
	}
	return project, nil
}

func (c *MusicProjectGatewayClient) doJSON(ctx context.Context, method, path string, body []byte, idempotencyKey string, expectedStatus int, allowUnknown bool, target any) error {
	endpoint := *c.endpoint
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build music gateway request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Music-Expected-Actor", c.actorID)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)
		// The gateway key makes the operation idempotent at the service boundary;
		// keep the transport from replaying a POST after an ambiguous network error.
		req.GetBody = nil
	}
	response, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("music gateway request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return &MusicGatewayStatusError{StatusCode: response.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("music gateway response is not JSON")
	}
	if response.ContentLength > maxMusicGatewayResponse {
		return errors.New("music gateway response exceeds the configured limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxMusicGatewayResponse+1))
	if err != nil {
		return fmt.Errorf("read music gateway response: %w", err)
	}
	if len(data) > maxMusicGatewayResponse {
		return errors.New("music gateway response exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !allowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode music gateway response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("music gateway response contains trailing JSON")
	}
	return nil
}

func validateMusicProject(project MusicProject) error {
	if err := validateMusicProjectID(project.ID); err != nil {
		return errors.New("music project response is missing a valid opaque ID")
	}
	if len([]rune(project.Title)) > 200 || len([]rune(project.Brief)) > 10000 || project.Brief == "" || project.HeadRevision < 0 || project.State == "" {
		return errors.New("music project response exceeds the supported schema")
	}
	if project.IntendedUse != "personal_noncommercial" || project.Mode != "interactive" {
		return errors.New("music project response contains unsupported project defaults")
	}
	return nil
}

func validateMusicProjectID(projectID string) error {
	if projectID == "" || len(projectID) > 256 || !musicProjectIDPattern.MatchString(projectID) {
		return errors.New("music project ID is invalid")
	}
	return nil
}

var musicProjectIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
