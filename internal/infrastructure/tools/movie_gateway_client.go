package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	movieGatewayRequestTimeout = 8 * time.Second
	movieAssetRequestTimeout   = 2 * time.Minute
	movieTimingRequestTimeout  = 30 * time.Second
	movieDownloadTimeout       = 5 * time.Minute
	maxMovieGatewayResponse    = 2 << 20
	maxMovieAssetBytes         = 64 << 20
	maxMovieRenderRequest      = 1 << 20
	maxMovieTimingRequest      = (2 << 20) + (16 << 10)
)

var movieAssetIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var movieFailureCodePattern = regexp.MustCompile(`^[A-Z0-9_.-]{1,64}$`)
var movieBackendCapabilityPattern = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

type MovieGatewayClientConfig struct {
	Endpoint    string
	ActorID     string
	BearerToken string
	HTTPClient  *http.Client
}

type MovieGatewayStatusError struct {
	StatusCode int
	Code       string
}

func (e *MovieGatewayStatusError) Error() string {
	if e == nil {
		return "Movie gateway request failed"
	}
	return fmt.Sprintf("Movie gateway returned HTTP %d", e.StatusCode)
}

func (e *MovieGatewayStatusError) MovieGatewayStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

type MovieGatewayClient struct {
	endpoint *url.URL
	actorID  string
	token    string
	client   *http.Client
}

var _ MovieGateway = (*MovieGatewayClient)(nil)

func NewMovieGatewayClient(cfg MovieGatewayClientConfig) (*MovieGatewayClient, error) {
	endpoint, err := parseMovieGatewayEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	actorID := strings.TrimSpace(cfg.ActorID)
	if actorID == "" || len(actorID) > 200 || strings.ContainsAny(actorID, "\r\n\x00") {
		return nil, errors.New("movie actor_id is invalid")
	}
	token := cfg.BearerToken
	if len(token) < 32 || len(token) > 4096 || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("movie bearer token is invalid")
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
	client.Timeout = movieGatewayRequestTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &MovieGatewayClient{endpoint: endpoint, actorID: actorID, token: token, client: client}, nil
}

func parseMovieGatewayEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("movie endpoint must be an absolute numeric-loopback HTTP origin")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("movie endpoint must use a numeric loopback address")
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("movie endpoint must include a valid port")
	}
	endpoint.Path = ""
	endpoint.RawPath = ""
	return endpoint, nil
}

func (c *MovieGatewayClient) ActorID() string { return c.actorID }

func (c *MovieGatewayClient) ProbeCapabilities(ctx context.Context) (MovieCapabilities, error) {
	var capabilities MovieCapabilities
	if err := c.doJSON(ctx, c.client, http.MethodGet, "/v1/movie/capabilities", nil, "", []int{http.StatusOK}, maxMovieGatewayResponse, &capabilities); err != nil {
		return MovieCapabilities{}, err
	}
	if err := validateMovieCapabilities(capabilities); err != nil {
		return MovieCapabilities{}, err
	}
	if capabilities.ActorID != c.actorID {
		return MovieCapabilities{}, errors.New("Movie capabilities do not confirm the configured actor")
	}
	return capabilities, nil
}

func (c *MovieGatewayClient) CreateMovieAsset(ctx context.Context, mediaType, sourceModule, sourceID, expectedSHA256 string, data []byte, idempotencyKey string) (MovieAsset, error) {
	if err := validateMovieIdempotencyKey(idempotencyKey); err != nil {
		return MovieAsset{}, err
	}
	if sourceModule != "music" || !validMusicOpaqueID(sourceID) || !musicArtifactSHA256Pattern.MatchString(expectedSHA256) ||
		len(data) < 1 || len(data) > maxMovieAssetBytes || !movieAudioMediaType(mediaType) {
		return MovieAsset{}, errors.New("Movie asset import metadata or bytes are outside the supported contract")
	}
	actualHash := sha256.Sum256(data)
	if hex.EncodeToString(actualHash[:]) != expectedSHA256 {
		return MovieAsset{}, errors.New("Movie asset import checksum does not match the Music artifact")
	}
	client := *c.client
	client.Timeout = movieAssetRequestTimeout
	requestCtx, cancel := context.WithTimeout(ctx, movieAssetRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint.String()+"/v1/movie/assets", bytes.NewReader(data))
	if err != nil {
		return MovieAsset{}, errors.New("build Movie asset import request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Movie-Expected-Actor", c.actorID)
	req.Header.Set("X-Source-Module", sourceModule)
	req.Header.Set("X-Source-ID", sourceID)
	req.Header.Set("X-Expected-SHA256", expectedSHA256)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.GetBody = nil
	response, err := client.Do(req)
	if err != nil {
		return MovieAsset{}, fmt.Errorf("Movie asset import request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return MovieAsset{}, movieGatewayStatusError(response)
	}
	var asset MovieAsset
	if err := decodeMovieJSONResponse(response, maxMovieGatewayResponse, &asset); err != nil {
		return MovieAsset{}, err
	}
	if err := validateMovieAsset(asset); err != nil {
		return MovieAsset{}, err
	}
	if asset.SHA256 != expectedSHA256 || asset.MediaType != canonicalMovieAudioType(mediaType) || asset.SourceModule != sourceModule || asset.SourceID != sourceID || asset.SizeBytes != int64(len(data)) {
		return MovieAsset{}, errors.New("Movie asset receipt does not match the imported Music artifact")
	}
	return asset, nil
}

func (c *MovieGatewayClient) GetMovieAssetMetadata(ctx context.Context, assetID string) (MovieAsset, error) {
	if !movieAssetIDPattern.MatchString(assetID) {
		return MovieAsset{}, errors.New("Movie asset ID is invalid")
	}
	var asset MovieAsset
	path := "/v1/movie/assets/" + url.PathEscape(assetID) + "/metadata"
	if err := c.doJSON(ctx, c.client, http.MethodGet, path, nil, "", []int{http.StatusOK}, maxMovieGatewayResponse, &asset); err != nil {
		return MovieAsset{}, err
	}
	if err := validateMovieAsset(asset); err != nil {
		return MovieAsset{}, err
	}
	if asset.ID != assetID {
		return MovieAsset{}, errors.New("Movie asset metadata ID does not match the request")
	}
	return asset, nil
}

func (c *MovieGatewayClient) CreateMovieTimings(ctx context.Context, analysis MusicAnalysis, fps int) (MovieTimingReceipt, error) {
	if err := validateMusicAnalysis(analysis); err != nil || analysis.State != "succeeded" || fps < 1 || fps > 60 {
		return MovieTimingReceipt{}, errors.New("Movie timing input is not a successful supported Music analysis")
	}
	body, err := json.Marshal(struct {
		Analysis MusicAnalysis `json:"analysis"`
		FPS      int           `json:"fps"`
	}{Analysis: analysis, FPS: fps})
	if err != nil || len(body) > maxMovieTimingRequest {
		return MovieTimingReceipt{}, errors.New("Movie timing request exceeds the configured limit")
	}
	client := *c.client
	client.Timeout = movieTimingRequestTimeout
	requestCtx, cancel := context.WithTimeout(ctx, movieTimingRequestTimeout)
	defer cancel()
	var receipt MovieTimingReceipt
	if err := c.doJSON(requestCtx, &client, http.MethodPost, "/v1/movie/timings", body, "", []int{http.StatusOK}, maxMovieGatewayResponse, &receipt); err != nil {
		return MovieTimingReceipt{}, err
	}
	if err := validateMovieTimingReceipt(receipt, analysis, fps); err != nil {
		return MovieTimingReceipt{}, err
	}
	return receipt, nil
}

func (c *MovieGatewayClient) SubmitMovieRender(ctx context.Context, plan MovieRenderPlan, idempotencyKey string) (MovieRenderJob, error) {
	if err := validateMovieIdempotencyKey(idempotencyKey); err != nil {
		return MovieRenderJob{}, err
	}
	if err := validateMovieRenderPlan(plan, false); err != nil {
		return MovieRenderJob{}, err
	}
	body, err := json.Marshal(plan)
	if err != nil || len(body) > maxMovieRenderRequest {
		return MovieRenderJob{}, errors.New("Movie render request exceeds the configured limit")
	}
	var job MovieRenderJob
	if err := c.doJSON(ctx, c.client, http.MethodPost, "/v1/movie/renders", body, idempotencyKey, []int{http.StatusAccepted}, maxMovieGatewayResponse, &job); err != nil {
		return MovieRenderJob{}, err
	}
	if err := validateMovieRenderJob(job); err != nil {
		return MovieRenderJob{}, errors.New("Movie render response does not match the persisted-job contract")
	}
	return job, nil
}

func (c *MovieGatewayClient) GetMovieRenderJob(ctx context.Context, jobID string) (MovieRenderJob, error) {
	if !movieAssetIDPattern.MatchString(jobID) {
		return MovieRenderJob{}, errors.New("Movie render job ID is invalid")
	}
	var job MovieRenderJob
	path := "/v1/movie/jobs/" + url.PathEscape(jobID)
	if err := c.doJSON(ctx, c.client, http.MethodGet, path, nil, "", []int{http.StatusOK}, maxMovieGatewayResponse, &job); err != nil {
		return MovieRenderJob{}, err
	}
	if err := validateMovieRenderJob(job); err != nil || job.ID != jobID {
		return MovieRenderJob{}, errors.New("Movie render job response does not match the requested ID")
	}
	return job, nil
}

func (c *MovieGatewayClient) OpenMovieRender(ctx context.Context, jobID, rangeHeader string) (*http.Response, error) {
	if !movieAssetIDPattern.MatchString(jobID) || (rangeHeader != "" && (len(rangeHeader) > 256 || !movieDownloadRangePattern.MatchString(rangeHeader))) {
		return nil, errors.New("Movie render download request is invalid")
	}
	requestCtx, cancel := context.WithTimeout(ctx, movieDownloadTimeout)
	// The returned response owns the request context until its body is closed.
	endpoint := *c.endpoint
	endpoint.Path = "/v1/movie/jobs/" + url.PathEscape(jobID) + "/download"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		cancel()
		return nil, errors.New("build Movie render download request")
	}
	req.Header.Set("Accept", "video/mp4")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Movie-Expected-Actor", c.actorID)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	client := *c.client
	client.Timeout = 0
	response, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("Movie render download request failed: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		defer response.Body.Close()
		cancel()
		return nil, movieGatewayStatusError(response)
	}
	response.Body = &cancelReadCloser{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}

var movieDownloadRangePattern = regexp.MustCompile(`^bytes=(?:[0-9]+-[0-9]*|-[0-9]+)$`)

func (c *MovieGatewayClient) doJSON(ctx context.Context, client *http.Client, method, path string, body []byte, idempotencyKey string, expectedStatuses []int, maxBytes int64, target any) error {
	return c.doJSONContent(ctx, client, method, path, body, "application/json", idempotencyKey, expectedStatuses, maxBytes, target)
}

func (c *MovieGatewayClient) doJSONContent(ctx context.Context, client *http.Client, method, path string, body []byte, contentType, idempotencyKey string, expectedStatuses []int, maxBytes int64, target any) error {
	endpoint := *c.endpoint
	endpoint.Path = path
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("build Movie gateway request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Movie-Expected-Actor", c.actorID)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", contentType)
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		req.GetBody = nil
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Movie gateway request failed: %w", err)
	}
	defer response.Body.Close()
	if !containsHTTPStatus(expectedStatuses, response.StatusCode) {
		return movieGatewayStatusError(response)
	}
	return decodeMovieJSONResponse(response, maxBytes, target)
}

func decodeMovieJSONResponse(response *http.Response, maxBytes int64, target any) error {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("Movie gateway response is not JSON")
	}
	if response.ContentLength > maxBytes {
		return errors.New("Movie gateway response exceeds the configured limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return errors.New("read Movie gateway response")
	}
	if int64(len(data)) > maxBytes {
		return errors.New("Movie gateway response exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("Movie gateway response is invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Movie gateway response contains trailing JSON")
	}
	return nil
}

func movieGatewayStatusError(response *http.Response) error {
	statusErr := &MovieGatewayStatusError{StatusCode: response.StatusCode}
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
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil && movieFailureCodePattern.MatchString(envelope.Error.Code) {
		statusErr.Code = envelope.Error.Code
	}
	return statusErr
}

func validateMovieCapabilities(capabilities MovieCapabilities) error {
	caps := capabilities.Capabilities
	if capabilities.ActorID == "" || (caps.AssetStorage != "available" && caps.AssetStorage != "disabled") ||
		(caps.Rendering != "available" && caps.Rendering != "disabled") || (caps.RenderQueue != "available" && caps.RenderQueue != "disabled") ||
		(caps.MusicTiming != "available" && caps.MusicTiming != "disabled") || caps.ProductAcceptance || len(caps.Backend.Reason) > 512 ||
		(caps.Rendering == "available") != caps.Backend.Available {
		return errors.New("Movie capability response does not match the supported schema")
	}
	if caps.Backend.Capabilities == nil || caps.Backend.Available && (!caps.Backend.Configured || !caps.Backend.Probed) {
		return errors.New("Movie backend capability state is inconsistent")
	}
	seen := make(map[string]struct{}, len(caps.Backend.Capabilities))
	for _, capability := range caps.Backend.Capabilities {
		if !movieBackendCapabilityPattern.MatchString(capability.Name) || (capability.Status != "available" && capability.Status != "unavailable") || len(capability.Reason) > 512 {
			return errors.New("Movie backend capability response contains an invalid entry")
		}
		if _, exists := seen[capability.Name]; exists {
			return errors.New("Movie backend capability response contains a duplicate entry")
		}
		seen[capability.Name] = struct{}{}
	}
	return nil
}

func validateMovieAsset(asset MovieAsset) error {
	if !movieAssetIDPattern.MatchString(asset.ID) || !musicArtifactSHA256Pattern.MatchString(asset.SHA256) || asset.SizeBytes < 1 || asset.SizeBytes > 100<<20 ||
		!movieAssetMediaTypeAllowed(asset.MediaType) || (asset.SourceModule != "image" && asset.SourceModule != "music" && asset.SourceModule != "upload" && asset.SourceModule != "unknown") {
		return errors.New("Movie asset response does not match the supported schema")
	}
	if (asset.SourceModule == "music" || asset.SourceModule == "image") && !validMusicOpaqueID(asset.SourceID) || asset.SourceModule != "music" && asset.SourceModule != "image" && asset.SourceID != "" && !validMusicOpaqueID(asset.SourceID) {
		return errors.New("Movie asset response has invalid source provenance")
	}
	if _, err := time.Parse(time.RFC3339Nano, asset.CreatedAt); err != nil {
		return errors.New("Movie asset response has an invalid creation time")
	}
	return nil
}

func validateMovieTimingReceipt(receipt MovieTimingReceipt, analysis MusicAnalysis, fps int) error {
	var document MusicAnalysisDocument
	if err := json.Unmarshal(analysis.Result, &document); err != nil {
		return errors.New("Music analysis result is invalid for Movie timing")
	}
	if receipt.Status != "succeeded" || receipt.FPS != fps || receipt.SourceSampleRate != document.SampleRate || receipt.SourceDurationFrames != document.DurationFrames ||
		receipt.VideoDurationFrames < 1 || receipt.VideoDurationFrames > int64(fps)*180 || receipt.BeatFrames == nil || receipt.Captions == nil ||
		len(receipt.BeatFrames) > 5000 || len(receipt.Captions) > 500 {
		return errors.New("Movie timing receipt does not match its Music source analysis")
	}
	if analysis.Operation == "beats" && len(receipt.Captions) != 0 || analysis.Operation == "captions" && len(receipt.BeatFrames) != 0 {
		return errors.New("Movie timing receipt contains fields from the other analysis operation")
	}
	for i, frame := range receipt.BeatFrames {
		if frame < 0 || frame >= receipt.VideoDurationFrames || i > 0 && frame <= receipt.BeatFrames[i-1] {
			return errors.New("Movie timing receipt contains invalid beat frames")
		}
	}
	for _, caption := range receipt.Captions {
		if caption.StartFrame < 0 || caption.EndFrame <= caption.StartFrame || caption.EndFrame > receipt.VideoDurationFrames || strings.TrimSpace(caption.Text) == "" || len([]rune(caption.Text)) > 4000 {
			return errors.New("Movie timing receipt contains invalid captions")
		}
	}
	return nil
}

func validateMovieRenderJob(job MovieRenderJob) error {
	if !movieAssetIDPattern.MatchString(job.ID) || (job.State != "queued" && job.State != "running" && job.State != "succeeded" && job.State != "failed" && job.State != "needs_reconciliation") {
		return errors.New("Movie render job response does not match the supported schema")
	}
	if _, err := time.Parse(time.RFC3339Nano, job.CreatedAt); err != nil {
		return errors.New("Movie render job has an invalid creation time")
	}
	if _, err := time.Parse(time.RFC3339Nano, job.UpdatedAt); err != nil {
		return errors.New("Movie render job has an invalid update time")
	}
	if job.State == "succeeded" {
		if !movieAssetIDPattern.MatchString(job.ArtifactID) || !musicArtifactSHA256Pattern.MatchString(job.SHA256) || job.SizeBytes < 1 || job.SizeBytes > 1<<30 || job.FailureCode != "" {
			return errors.New("successful Movie render job is missing valid output metadata")
		}
	} else if job.ArtifactID != "" || job.SHA256 != "" || job.SizeBytes != 0 {
		return errors.New("unfinished Movie render job unexpectedly contains output metadata")
	}
	if job.FailureCode != "" && !movieFailureCodePattern.MatchString(job.FailureCode) || (job.State == "queued" || job.State == "running" || job.State == "succeeded") && job.FailureCode != "" {
		return errors.New("Movie render job failure metadata is inconsistent")
	}
	return nil
}

func validateMovieRenderPlan(plan MovieRenderPlan, allowMissingTimeline bool) error {
	if plan.SchemaVersion != 1 || plan.Width < 16 || plan.Width > 3840 || plan.Width%2 != 0 || plan.Height < 16 || plan.Height > 2160 || plan.Height%2 != 0 ||
		plan.FPS < 1 || plan.FPS > 60 || len([]rune(plan.Title)) < 1 || len([]rune(plan.Title)) > 200 || len(plan.Scenes) < 1 || len(plan.Scenes) > 120 {
		return errors.New("Movie render plan metadata is outside the supported range")
	}
	duration := 0
	if plan.DurationFrames != nil {
		duration = *plan.DurationFrames
		if duration < 1 || duration > plan.FPS*180 {
			return errors.New("Movie render plan duration is outside the supported range")
		}
	} else if !allowMissingTimeline {
		return errors.New("Movie render plan duration is required")
	}
	if plan.AudioAssetID != "" && !movieAssetIDPattern.MatchString(plan.AudioAssetID) {
		return errors.New("Movie render plan audio asset ID is invalid")
	}
	for index, frame := range plan.BeatFrames {
		if frame < 0 || duration > 0 && frame >= int64(duration) || index > 0 && frame <= plan.BeatFrames[index-1] {
			return errors.New("Movie render plan beat frames are invalid")
		}
	}
	if len(plan.Captions) > 500 {
		return errors.New("Movie render plan has too many captions")
	}
	for _, caption := range plan.Captions {
		if caption.StartFrame < 0 || caption.EndFrame <= caption.StartFrame || duration > 0 && caption.EndFrame > int64(duration) || len([]rune(caption.Text)) < 1 || len([]rune(caption.Text)) > 4000 {
			return errors.New("Movie render plan caption is invalid")
		}
	}
	for _, scene := range plan.Scenes {
		if scene.StartFrame < 0 || scene.StartFrame > plan.FPS*180 || scene.DurationFrames != nil && (*scene.DurationFrames < 1 || *scene.DurationFrames > plan.FPS*180) {
			return errors.New("Movie render plan scene timing is invalid")
		}
		if scene.DurationFrames == nil && !allowMissingTimeline {
			return errors.New("Movie render plan scene duration is required")
		}
		if duration > 0 && scene.DurationFrames != nil && scene.StartFrame+*scene.DurationFrames > duration {
			return errors.New("Movie render plan scene extends beyond the timeline")
		}
		switch scene.Kind {
		case "title":
			if scene.AssetID != "" || len([]rune(scene.Text)) < 1 || len([]rune(scene.Text)) > 4000 {
				return errors.New("Movie title scene fields are invalid")
			}
		case "image", "video", "lottie":
			if !movieAssetIDPattern.MatchString(scene.AssetID) || len([]rune(scene.Text)) > 4000 {
				return errors.New("Movie asset scene fields are invalid")
			}
		case "three":
			if scene.AssetID != "" || len([]rune(scene.Text)) > 4000 {
				return errors.New("Movie three scene fields are invalid")
			}
		default:
			return errors.New("Movie render plan scene kind is unsupported")
		}
	}
	return nil
}

func validateMovieIdempotencyKey(key string) error {
	if len(key) < 1 || len(key) > 200 || strings.TrimSpace(key) != key {
		return errors.New("Movie idempotency key is invalid")
	}
	for _, char := range []byte(key) {
		if char < 0x21 || char > 0x7e {
			return errors.New("Movie idempotency key must contain printable ASCII")
		}
	}
	return nil
}

func movieAudioMediaType(mediaType string) bool {
	switch mediaType {
	case "audio/flac", "audio/x-flac", "audio/wav", "audio/x-wav", "audio/wave":
		return true
	default:
		return false
	}
}

func canonicalMovieAudioType(mediaType string) string {
	if mediaType == "audio/flac" || mediaType == "audio/x-flac" {
		return "audio/flac"
	}
	return "audio/wav"
}

func movieAssetMediaTypeAllowed(mediaType string) bool {
	switch mediaType {
	case "image/png", "image/jpeg", "video/mp4", "audio/wav", "audio/flac", "application/json":
		return true
	default:
		return false
	}
}
