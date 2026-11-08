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
	if err := validateMusicJobStatus(accepted); err != nil || accepted.ProjectID != projectID || accepted.PlanID != input.PlanID || accepted.PlanHash != input.ExpectedPlanHash {
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
		validateMusicPlanID(status.PlanID) != nil || !musicPlanHashPattern.MatchString(status.PlanHash) || !isMusicJobState(status.State) {
		return errors.New("music job response does not match the supported schema")
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
