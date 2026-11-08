package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	maxMusicPlanRequest        = 96 << 10
	maxMusicControlRequest     = 96 << 10
	maxMusicGenerationRequest  = 8 << 10
	maxMusicArtifactBytes      = 64 << 20
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
	Code       string
	Retryable  bool
}

func (e *MusicGatewayStatusError) Error() string {
	return fmt.Sprintf("music gateway returned HTTP %d", e.StatusCode)
}

func (e *MusicGatewayStatusError) MusicGatewayStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
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
var _ MusicPlanGateway = (*MusicProjectGatewayClient)(nil)
var _ MusicGenerationGateway = (*MusicProjectGatewayClient)(nil)
var _ MusicRemixGateway = (*MusicProjectGatewayClient)(nil)
var _ MusicArtifactPlaybackGateway = (*MusicProjectGatewayClient)(nil)

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

func (c *MusicProjectGatewayClient) GetMusicControl(ctx context.Context, projectID string) (MusicControlState, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicControlState{}, err
	}
	var state MusicControlState
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/control"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, false, &state); err != nil {
		return MusicControlState{}, err
	}
	if err := validateMusicControlState(state, projectID); err != nil {
		return MusicControlState{}, err
	}
	return state, nil
}

func (c *MusicProjectGatewayClient) UpdateMusicControl(ctx context.Context, projectID string, input MusicControlUpdate, idempotencyKey string) (MusicControlState, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicControlState{}, err
	}
	if err := validateMusicControlUpdate(input); err != nil {
		return MusicControlState{}, err
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicControlState{}, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicControlRequest {
		return MusicControlState{}, errors.New("music control update is invalid or exceeds the configured limit")
	}
	var state MusicControlState
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/control"
	if err := c.doJSON(ctx, http.MethodPost, path, body, idempotencyKey, http.StatusOK, false, &state); err != nil {
		return MusicControlState{}, err
	}
	if err := validateMusicControlState(state, projectID); err != nil {
		return MusicControlState{}, err
	}
	return state, nil
}

func (c *MusicProjectGatewayClient) CreateMusicControlRequest(ctx context.Context, projectID string, input MusicControlRequestInput, idempotencyKey string) (MusicControlRequest, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicControlRequest{}, err
	}
	if err := validateMusicControlRequestInput(input); err != nil {
		return MusicControlRequest{}, err
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicControlRequest{}, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicControlRequest {
		return MusicControlRequest{}, errors.New("music control request is invalid or exceeds the configured limit")
	}
	var request MusicControlRequest
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/requests"
	if err := c.doJSON(ctx, http.MethodPost, path, body, idempotencyKey, http.StatusCreated, false, &request); err != nil {
		return MusicControlRequest{}, err
	}
	if err := validateMusicControlRequest(request, projectID, ""); err != nil {
		return MusicControlRequest{}, err
	}
	if request.Operation != input.Operation {
		return MusicControlRequest{}, errors.New("music control request response operation does not match the request")
	}
	return request, nil
}

func (c *MusicProjectGatewayClient) GetMusicControlRequest(ctx context.Context, projectID, requestID string) (MusicControlRequest, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicControlRequest{}, err
	}
	if err := validateMusicRequestID(requestID); err != nil {
		return MusicControlRequest{}, err
	}
	var request MusicControlRequest
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/requests/" + url.PathEscape(requestID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, false, &request); err != nil {
		return MusicControlRequest{}, err
	}
	if err := validateMusicControlRequest(request, projectID, requestID); err != nil {
		return MusicControlRequest{}, err
	}
	return request, nil
}

func (c *MusicProjectGatewayClient) CreateProductionPlan(ctx context.Context, projectID string, input MusicProductionPlanInput, idempotencyKey string) (MusicProductionPlan, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicProductionPlan{}, err
	}
	if len(idempotencyKey) == 0 || len(idempotencyKey) > 200 || strings.TrimSpace(idempotencyKey) != idempotencyKey {
		return MusicProductionPlan{}, errors.New("music idempotency key is invalid")
	}
	if input.Kind != "production" || input.SchemaVersion != "1.0" || input.SourceRequestID == "" || len(input.SourceRequestID) > 200 {
		return MusicProductionPlan{}, errors.New("music production plan identity is invalid")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return MusicProductionPlan{}, fmt.Errorf("encode music production plan request: %w", err)
	}
	if len(body) > maxMusicPlanRequest {
		return MusicProductionPlan{}, errors.New("music production plan request exceeds the configured limit")
	}
	var plan MusicProductionPlan
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/plans"
	if err := c.doJSON(ctx, http.MethodPost, path, body, idempotencyKey, http.StatusCreated, false, &plan); err != nil {
		return MusicProductionPlan{}, err
	}
	if err := validateMusicProductionPlan(plan); err != nil {
		return MusicProductionPlan{}, err
	}
	if plan.ProjectID != projectID || plan.SourceRequestID != input.SourceRequestID {
		return MusicProductionPlan{}, errors.New("music production plan response identity does not match the request")
	}
	return plan, nil
}

func (c *MusicProjectGatewayClient) GetProductionPlan(ctx context.Context, planID string) (MusicProductionPlan, error) {
	if err := validateMusicPlanID(planID); err != nil {
		return MusicProductionPlan{}, err
	}
	var plan MusicProductionPlan
	path := "/v1/music/plans/" + url.PathEscape(planID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, false, &plan); err != nil {
		return MusicProductionPlan{}, err
	}
	if err := validateMusicProductionPlan(plan); err != nil {
		return MusicProductionPlan{}, err
	}
	if plan.PlanID != planID {
		return MusicProductionPlan{}, errors.New("music production plan response ID does not match the requested plan")
	}
	return plan, nil
}

func (c *MusicProjectGatewayClient) SubmitMusicGeneration(ctx context.Context, projectID string, input MusicGenerationRequest, idempotencyKey string) (MusicGenerationAccepted, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicGenerationAccepted{}, err
	}
	if err := validateMusicPlanID(input.PlanID); err != nil || !musicPlanHashPattern.MatchString(input.ExpectedPlanHash) {
		return MusicGenerationAccepted{}, errors.New("music generation plan identity is invalid")
	}
	if input.RequestID != "" && validateMusicRequestID(input.RequestID) != nil {
		return MusicGenerationAccepted{}, errors.New("music generation request ID is invalid")
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicGenerationAccepted{}, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicGenerationRequest {
		return MusicGenerationAccepted{}, errors.New("music generation request is invalid or exceeds the configured limit")
	}
	var accepted MusicGenerationAccepted
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/generations"
	if err := c.doJSON(ctx, http.MethodPost, path, body, idempotencyKey, http.StatusAccepted, false, &accepted); err != nil {
		return MusicGenerationAccepted{}, err
	}
	if err := validateMusicJobStatus(accepted); err != nil || accepted.ProjectID != projectID || accepted.PlanID != input.PlanID || accepted.PlanHash != input.ExpectedPlanHash ||
		(input.RequestID != "" && accepted.RequestID != input.RequestID) {
		return MusicGenerationAccepted{}, errors.New("music generation acceptance does not match the request")
	}
	return accepted, nil
}

func (c *MusicProjectGatewayClient) GetMusicJob(ctx context.Context, jobID string) (MusicJobStatus, error) {
	if err := validateMusicJobID(jobID); err != nil {
		return MusicJobStatus{}, err
	}
	var status MusicJobStatus
	path := "/v1/music/jobs/" + url.PathEscape(jobID)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, "", http.StatusOK, false, &status); err != nil {
		return MusicJobStatus{}, err
	}
	if err := validateMusicJobStatus(status); err != nil {
		return MusicJobStatus{}, err
	}
	if status.JobID != jobID {
		return MusicJobStatus{}, errors.New("music job response ID does not match the request")
	}
	return status, nil
}

func (c *MusicProjectGatewayClient) CreateMusicEdit(ctx context.Context, projectID string, input MusicEditInput, idempotencyKey string) (MusicArtifact, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicArtifact{}, err
	}
	if err := validateMusicArtifactID(input.SourceArtifactID); err != nil {
		return MusicArtifact{}, err
	}
	if err := validateMusicEditInput(input); err != nil {
		return MusicArtifact{}, err
	}
	if input.RequestID != "" && validateMusicRequestID(input.RequestID) != nil {
		return MusicArtifact{}, errors.New("music remix request ID is invalid")
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicArtifact{}, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicGenerationRequest {
		return MusicArtifact{}, errors.New("music remix request is invalid or exceeds the configured limit")
	}
	var artifact MusicArtifact
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/edits"
	if err := c.doJSON(ctx, http.MethodPost, path, body, idempotencyKey, http.StatusCreated, false, &artifact); err != nil {
		return MusicArtifact{}, err
	}
	if err := validateMusicArtifact(artifact); err != nil {
		return MusicArtifact{}, err
	}
	if artifact.ProjectID != projectID || artifact.Kind != "audition_remix" || artifact.SourceArtifactID != input.SourceArtifactID {
		return MusicArtifact{}, errors.New("music remix response identity does not match the request")
	}
	return artifact, nil
}

func (c *MusicProjectGatewayClient) OpenMusicArtifact(ctx context.Context, artifactID, rangeHeader string) (*http.Response, error) {
	if err := validateMusicArtifactID(artifactID); err != nil {
		return nil, err
	}
	if len(rangeHeader) > 256 || strings.ContainsAny(rangeHeader, "\r\n\x00") {
		return nil, errors.New("music artifact Range header is invalid")
	}
	endpoint := *c.endpoint
	endpoint.Path = "/v1/music/artifacts/" + url.PathEscape(artifactID) + "/download"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("build music artifact request")
	}
	req.Header.Set("Accept", "audio/*")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Music-Expected-Actor", c.actorID)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("music artifact request failed: %w", err)
	}
	return response, nil
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
		return musicGatewayStatusError(response)
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

func musicGatewayStatusError(response *http.Response) error {
	statusErr := &MusicGatewayStatusError{StatusCode: response.StatusCode}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || response.ContentLength > 4096 {
		return statusErr
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return statusErr
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || !allowlistedMusicGatewayErrorCode(envelope.Error.Code) {
		return statusErr
	}
	statusErr.Code = envelope.Error.Code
	statusErr.Retryable = envelope.Error.Retryable
	return statusErr
}

func allowlistedMusicGatewayErrorCode(code string) bool {
	switch code {
	case "MUSIC_NEEDS_CONFIRMATION", "MUSIC_PRESERVATION_CONFLICT", "MUSIC_CONTROL_CONFLICT", "MUSIC_CONTROL_STALE", "MUSIC_UNSUPPORTED_OPERATION":
		return true
	default:
		return false
	}
}

func validateMusicControlState(state MusicControlState, projectID string) error {
	if state.ProjectID != projectID || state.Revision < 0 || !validMusicMode(state.Mode) || !validMusicModeSource(state.ModeSource) ||
		(state.UpdatedAt.IsZero() && !(state.Revision == 0 && state.ModeSource == "default")) {
		return errors.New("music control response does not match the supported schema or requested project")
	}
	if len(state.Locks) > 128 || len(state.HardConstraints) > 128 {
		return errors.New("music control response exceeds the supported collection limits")
	}
	if err := validateMusicLocks(state.Locks, false); err != nil {
		return errors.New("music control response contains an invalid lock")
	}
	if err := validateMusicConstraints(state.HardConstraints); err != nil {
		return errors.New("music control response contains an invalid hard constraint")
	}
	if err := validateMusicHardConstraints(state.HardConstraints); err != nil {
		return errors.New("music control response contains an unsupported hard constraint")
	}
	for _, versionID := range []*string{state.AdoptedVersionID, state.RecommendedVersionID} {
		if versionID != nil && validateMusicArtifactID(*versionID) != nil {
			return errors.New("music control response contains an invalid version ID")
		}
	}
	return nil
}

func validateMusicControlUpdate(input MusicControlUpdate) error {
	if input.ExpectedRevision < 0 || len(input.AddLocks) > 128 || len(input.RemoveLockIDs) > 128 ||
		len(input.SetHardConstraints) > 128 || len(input.RemoveConstraintProperties) > 128 {
		return errors.New("music control update is outside the supported bounds")
	}
	if input.Mode == nil && len(input.AddLocks) == 0 && len(input.RemoveLockIDs) == 0 &&
		len(input.SetHardConstraints) == 0 && len(input.RemoveConstraintProperties) == 0 && input.SelectVersionID == nil {
		return errors.New("music control update has no changes")
	}
	if input.Mode != nil && !validMusicMode(*input.Mode) {
		return errors.New("music control mode is unsupported")
	}
	if err := validateMusicLocks(input.AddLocks, true); err != nil {
		return err
	}
	for _, lockID := range input.RemoveLockIDs {
		if !validMusicOpaqueID(lockID) {
			return errors.New("music control update contains an invalid lock ID")
		}
	}
	if err := validateMusicConstraints(input.SetHardConstraints); err != nil {
		return err
	}
	if err := validateMusicHardConstraints(input.SetHardConstraints); err != nil {
		return err
	}
	for _, property := range input.RemoveConstraintProperties {
		if !validMusicText(property, 128) {
			return errors.New("music control update contains an invalid constraint property")
		}
	}
	if input.SelectVersionID != nil && validateMusicArtifactID(*input.SelectVersionID) != nil {
		return errors.New("music control update contains an invalid version ID")
	}
	return nil
}

func validateMusicControlRequestInput(input MusicControlRequestInput) error {
	if !validMusicText(input.RawText, 20000) || (input.Operation != "generation" && input.Operation != "gain_remix") ||
		len(input.HardConstraints) > 128 || len(input.SoftPreferences) > 128 || len(input.PreserveLocks) > 128 || len(input.AutoScope) > 64 {
		return errors.New("music control request is outside the supported bounds")
	}
	if err := validateMusicRequestLayers(input.Layers); err != nil {
		return err
	}
	if input.ExplicitMode != nil && !validMusicMode(*input.ExplicitMode) {
		return errors.New("music control request explicit mode is unsupported")
	}
	if input.InferredMode != nil && !validMusicMode(*input.InferredMode) {
		return errors.New("music control request inferred mode is unsupported")
	}
	for _, layer := range input.Layers {
		if !validMusicText(layer, 200) {
			return errors.New("music control request contains an invalid layer")
		}
	}
	if input.BaseVersionID != nil && validateMusicArtifactID(*input.BaseVersionID) != nil {
		return errors.New("music control request contains an invalid base version ID")
	}
	if input.EditScope != nil && !validMusicFrameRange(*input.EditScope) {
		return errors.New("music control request contains an invalid edit scope")
	}
	if err := validateMusicConstraints(input.HardConstraints); err != nil {
		return err
	}
	if err := validateMusicHardConstraints(input.HardConstraints); err != nil {
		return err
	}
	if err := validateMusicConstraints(input.SoftPreferences); err != nil {
		return err
	}
	if err := validateMusicLocks(input.PreserveLocks, false); err != nil {
		return err
	}
	for _, scope := range input.AutoScope {
		if !validMusicText(scope, 128) {
			return errors.New("music control request contains an invalid auto scope")
		}
	}
	return nil
}

func validateMusicRequestLayers(layers []string) error {
	if len(layers) < 1 || len(layers) > 3 {
		return errors.New("Music request requires one to three L1/L2/L3 layer identifiers")
	}
	seen := make(map[string]struct{}, len(layers))
	for _, layer := range layers {
		if layer != "L1" && layer != "L2" && layer != "L3" {
			return errors.New("Music request layer must be L1, L2, or L3")
		}
		if _, exists := seen[layer]; exists {
			return errors.New("Music request layer identifiers must be unique")
		}
		seen[layer] = struct{}{}
	}
	return nil
}

func validateMusicHardConstraints(constraints []MusicConstraint) error {
	for _, constraint := range constraints {
		if constraint.Property != "output_format" && constraint.Property != "write_scope" {
			return errors.New("Music hard constraint property is unsupported")
		}
	}
	return nil
}

func validateMusicControlRequest(request MusicControlRequest, projectID, requestID string) error {
	if request.ProjectID != projectID || !validMusicOpaqueID(request.RequestID) || request.RequestID == "" ||
		(requestID != "" && request.RequestID != requestID) || request.RequestRevision < 1 || request.ControlRevision < 0 ||
		!validMusicMode(request.Mode) || !validMusicModeSource(request.ModeSource) || request.CreatedAt.IsZero() ||
		(request.Operation != "generation" && request.Operation != "gain_remix") || !validMusicText(request.RawText, 20000) {
		return errors.New("music control request response does not match the supported schema or requested identity")
	}
	if len(request.Layers) > 64 || len(request.EffectiveLocks) > 128 || len(request.EffectiveHardConstraints) > 128 {
		return errors.New("music control request response exceeds the supported collection limits")
	}
	if err := validateMusicLocks(request.EffectiveLocks, false); err != nil {
		return errors.New("music control request response contains an invalid effective lock")
	}
	if err := validateMusicConstraints(request.EffectiveHardConstraints); err != nil {
		return errors.New("music control request response contains an invalid effective constraint")
	}
	if err := validateMusicHardConstraints(request.EffectiveHardConstraints); err != nil {
		return errors.New("music control request response contains an unsupported effective constraint")
	}
	return nil
}

func validateMusicLocks(locks []MusicLock, allowMissingID bool) error {
	for _, lock := range locks {
		if lock.ID == "" && !allowMissingID || lock.ID != "" && !validMusicOpaqueID(lock.ID) {
			return errors.New("lock ID is invalid")
		}
		if validateMusicArtifactID(lock.ReferenceVersionID) != nil {
			return errors.New("lock reference version ID is invalid")
		}
		switch lock.Type {
		case "audio_exact", "structure", "perceptual":
		default:
			return errors.New("lock type is unsupported")
		}
		if lock.Stage != "" && lock.Stage != "final_mix" && lock.Stage != "track_audio" {
			return errors.New("lock stage is unsupported")
		}
		if lock.Type == "audio_exact" && lock.Stage != "" && lock.Stage != "final_mix" && lock.Stage != "track_audio" {
			return errors.New("exact audio lock stage is unsupported")
		}
		if lock.Scope != nil && !validMusicFrameRange(*lock.Scope) {
			return errors.New("lock scope is invalid")
		}
		if len(lock.Properties) > 64 {
			return errors.New("lock properties exceed the supported limit")
		}
		for _, property := range lock.Properties {
			if !validMusicText(property, 128) {
				return errors.New("lock property is invalid")
			}
		}
	}
	return nil
}

func validateMusicConstraints(constraints []MusicConstraint) error {
	for _, constraint := range constraints {
		if !validMusicText(constraint.Property, 128) || !validMusicText(constraint.Value, 2000) {
			return errors.New("constraint property or value is invalid")
		}
	}
	return nil
}

func validMusicFrameRange(scope MusicFrameRange) bool {
	return scope.StartFrame >= 0 && scope.EndFrame > scope.StartFrame && scope.EndFrame <= 60*60*48000
}

func validMusicMode(mode string) bool {
	return mode == "DIRECT" || mode == "AUTO"
}

func validMusicModeSource(source string) bool {
	switch source {
	case "explicit", "project", "inferred", "default":
		return true
	default:
		return false
	}
}

func validMusicText(value string, maxRunes int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= maxRunes && !strings.ContainsAny(value, "\x00")
}

func validMusicOpaqueID(value string) bool {
	return len(value) > 0 && len(value) <= 256 && musicProjectIDPattern.MatchString(value)
}

func validateMusicRequestID(requestID string) error {
	if !validMusicOpaqueID(requestID) {
		return errors.New("music request ID is invalid")
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

func validateMusicPlanID(planID string) error {
	if planID == "" || len(planID) > 256 || !musicProjectIDPattern.MatchString(planID) {
		return errors.New("music production plan ID is invalid")
	}
	return nil
}

func validateMusicJobID(jobID string) error {
	if jobID == "" || len(jobID) > 256 || !musicProjectIDPattern.MatchString(jobID) {
		return errors.New("music job ID is invalid")
	}
	return nil
}

func validateMusicArtifactID(artifactID string) error {
	if artifactID == "" || len(artifactID) > 256 || !musicProjectIDPattern.MatchString(artifactID) {
		return errors.New("music artifact ID is invalid")
	}
	return nil
}

func validateMusicIdempotencyKey(idempotencyKey string) error {
	if idempotencyKey == "" || len(idempotencyKey) > 200 || strings.TrimSpace(idempotencyKey) != idempotencyKey || strings.ContainsAny(idempotencyKey, "\r\n\x00") {
		return errors.New("music idempotency key is invalid")
	}
	return nil
}

func validateMusicJobStatus(status MusicJobStatus) error {
	if validateMusicJobID(status.JobID) != nil || validateMusicProjectID(status.ProjectID) != nil ||
		validateMusicPlanID(status.PlanID) != nil || !musicPlanHashPattern.MatchString(status.PlanHash) || !isMusicJobState(status.State) || status.ControlRevision < 0 {
		return errors.New("music job response does not match the supported schema")
	}
	if status.RequestID != "" && validateMusicRequestID(status.RequestID) != nil {
		return errors.New("music job response has an invalid request ID")
	}
	if _, err := time.Parse(time.RFC3339Nano, status.CreatedAt); err != nil {
		return errors.New("music job response has an invalid creation time")
	}
	if _, err := time.Parse(time.RFC3339Nano, status.UpdatedAt); err != nil {
		return errors.New("music job response has an invalid update time")
	}
	if status.FailureCode != "" && !musicFailureCodePattern.MatchString(status.FailureCode) {
		return errors.New("music job response has an invalid failure code")
	}
	if status.DurationFrames < 0 || status.DurationFrames > 60*60*48000 ||
		math.IsNaN(status.DurationActualS) || math.IsInf(status.DurationActualS, 0) || status.DurationActualS < 0 || status.DurationActualS > 3600 {
		return errors.New("music job response has invalid duration metadata")
	}
	if status.State == "succeeded" {
		if validateMusicArtifactID(status.ArtifactID) != nil || status.DurationActualS <= 0 {
			return errors.New("successful music job response is missing valid artifact metadata")
		}
	} else if status.ArtifactID != "" && validateMusicArtifactID(status.ArtifactID) != nil {
		return errors.New("music job response has an invalid artifact ID")
	}
	return nil
}

func validateMusicEditInput(input MusicEditInput) error {
	if input.StartFrame < 0 || input.EndFrame <= input.StartFrame || input.EndFrame > musicEditTimelineFrames ||
		input.FadeFrames < 0 || input.FadeFrames > musicEditMaxFadeFrames || input.FadeFrames*2 > input.EndFrame-input.StartFrame ||
		math.IsNaN(input.GainDB) || math.IsInf(input.GainDB, 0) || input.GainDB < -12 || input.GainDB > 12 {
		return errors.New("music edit request exceeds the bounded edit policy")
	}
	return nil
}

func validateMusicArtifact(artifact MusicArtifact) error {
	if validateMusicArtifactID(artifact.ID) != nil || validateMusicProjectID(artifact.ProjectID) != nil ||
		artifact.Kind == "" || len(artifact.Kind) > 64 || !musicArtifactSHA256Pattern.MatchString(artifact.SHA256) ||
		artifact.SizeBytes < 1 || artifact.SizeBytes > maxMusicArtifactBytes || artifact.DurationFrames < 1 || artifact.DurationFrames > 60*60*48000 ||
		artifact.SampleRate != 48000 || artifact.Channels != 2 || !isMusicAudioType(artifact.MediaType) {
		return errors.New("music artifact response does not match the supported schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, artifact.CreatedAt); err != nil {
		return errors.New("music artifact response has an invalid creation time")
	}
	if artifact.SourceArtifactID != "" && validateMusicArtifactID(artifact.SourceArtifactID) != nil {
		return errors.New("music artifact response has an invalid source artifact ID")
	}
	return nil
}

func isMusicAudioType(mediaType string) bool {
	return mediaType == "audio/flac" || mediaType == "audio/wav" || mediaType == "audio/x-wav"
}

func isMusicJobState(state string) bool {
	switch state {
	case "queued", "submitting", "running", "succeeded", "failed", "needs_reconciliation":
		return true
	default:
		return false
	}
}

func validateMusicProductionPlan(plan MusicProductionPlan) error {
	if err := validateMusicPlanID(plan.PlanID); err != nil {
		return errors.New("music production plan response has no valid opaque ID")
	}
	if err := validateMusicProjectID(plan.ProjectID); err != nil {
		return errors.New("music production plan response has no valid project ID")
	}
	if plan.Kind != "production" || plan.SchemaVersion != "1.0" || plan.SourceRequestID == "" || len(plan.SourceRequestID) > 200 || strings.TrimSpace(plan.BriefText) == "" || len([]rune(plan.BriefText)) > 10000 {
		return errors.New("music production plan response does not match schema 1.0")
	}
	if plan.IntendedUse != "personal_noncommercial" || !musicPlanHashPattern.MatchString(plan.PlanHash) || plan.ExecutionReason == "" {
		return errors.New("music production plan response contains unsupported metadata")
	}
	return nil
}

var musicProjectIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var musicPlanHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var musicArtifactSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
