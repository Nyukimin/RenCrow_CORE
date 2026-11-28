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
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

func (c *MusicProjectGatewayClient) CreateMusicAnalysis(ctx context.Context, projectID string, input MusicAnalysisRequest, idempotencyKey string) (MusicAnalysis, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicAnalysis{}, err
	}
	if err := validateMusicArtifactID(input.SourceArtifactID); err != nil {
		return MusicAnalysis{}, err
	}
	if input.Operation != "beats" && input.Operation != "captions" {
		return MusicAnalysis{}, errors.New("music analysis operation is unsupported")
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicAnalysis{}, err
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicGenerationRequest {
		return MusicAnalysis{}, errors.New("music analysis request exceeds the configured limit")
	}
	client := *c.client
	client.Timeout = musicAnalysisRequestTimeout
	requestCtx, cancel := context.WithTimeout(ctx, musicAnalysisRequestTimeout)
	defer cancel()
	var analysis MusicAnalysis
	path := "/v1/music/projects/" + url.PathEscape(projectID) + "/analyses"
	statusCode, err := c.doJSONResponseWithPolicy(requestCtx, &client, http.MethodPost, path, "", body, "application/json", idempotencyKey, []int{http.StatusCreated, http.StatusOK, http.StatusBadGateway}, maxMusicAnalysisResponse, false, &analysis)
	if err != nil {
		return MusicAnalysis{}, err
	}
	if err := validateMusicAnalysis(analysis); err != nil {
		return MusicAnalysis{}, err
	}
	stateMatchesStatus := false
	switch statusCode {
	case http.StatusCreated:
		stateMatchesStatus = analysis.State == "succeeded"
	case http.StatusOK:
		stateMatchesStatus = analysis.State == "succeeded" || analysis.State == "running" || analysis.State == "failed" || analysis.State == "needs_reconciliation"
	case http.StatusBadGateway:
		stateMatchesStatus = analysis.State == "failed"
	}
	if analysis.ProjectID != projectID || analysis.SourceArtifactID != input.SourceArtifactID || analysis.Operation != input.Operation || !stateMatchesStatus {
		return MusicAnalysis{}, errors.New("music analysis response does not match the request")
	}
	return analysis, nil
}

func (c *MusicProjectGatewayClient) GetMusicAnalysis(ctx context.Context, analysisID string) (MusicAnalysis, error) {
	if err := validateMusicOpaqueID(analysisID, "music analysis ID"); err != nil {
		return MusicAnalysis{}, err
	}
	var analysis MusicAnalysis
	path := "/v1/music/analyses/" + url.PathEscape(analysisID)
	if err := c.doJSONWithPolicy(ctx, c.client, http.MethodGet, path, "", nil, "application/json", "", []int{http.StatusOK}, maxMusicAnalysisResponse, false, &analysis); err != nil {
		return MusicAnalysis{}, err
	}
	if err := validateMusicAnalysis(analysis); err != nil {
		return MusicAnalysis{}, err
	}
	if analysis.AnalysisID != analysisID {
		return MusicAnalysis{}, errors.New("music analysis response ID does not match the request")
	}
	return analysis, nil
}

func (c *MusicProjectGatewayClient) SearchMusicSources(ctx context.Context, query string, page, pageSize int) (MusicSourceSearchResponse, error) {
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || len([]byte(query)) > 200 || page < 1 || page > 100 || pageSize < 1 || pageSize > 50 {
		return MusicSourceSearchResponse{}, errors.New("music source search arguments are outside the supported range")
	}
	values := url.Values{}
	values.Set("q", query)
	values.Set("page", fmt.Sprint(page))
	values.Set("page_size", fmt.Sprint(pageSize))
	var result MusicSourceSearchResponse
	if err := c.doJSONWithPolicy(ctx, c.client, http.MethodGet, "/v1/music/sources/openverse", values.Encode(), nil, "application/json", "", []int{http.StatusOK}, maxMusicSourceSearchResponse, false, &result); err != nil {
		return MusicSourceSearchResponse{}, err
	}
	if err := validateMusicSourceSearchResponse(result, page, pageSize); err != nil {
		return MusicSourceSearchResponse{}, err
	}
	return result, nil
}

func (c *MusicProjectGatewayClient) ImportMusicAudio(ctx context.Context, projectID, sourceAttachmentID, expectedSHA256 string, audio []byte, idempotencyKey string) (MusicAudioImportJobStatus, error) {
	if err := validateMusicProjectID(projectID); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if err := validateMusicOpaqueID(sourceAttachmentID, "source attachment ID"); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if !musicArtifactSHA256Pattern.MatchString(expectedSHA256) || len(audio) < 4 || len(audio) > maxMusicAudioImportBytes || string(audio[:4]) != "fLaC" {
		return MusicAudioImportJobStatus{}, errors.New("Music import accepts only bounded FLAC bytes with a verified SHA-256")
	}
	actualHash := sha256.Sum256(audio)
	if hex.EncodeToString(actualHash[:]) != expectedSHA256 {
		return MusicAudioImportJobStatus{}, errors.New("Music import attachment hash does not match its trusted metadata")
	}
	if err := validateMusicIdempotencyKey(idempotencyKey); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	path := "/v1/music/imports"
	client := *c.client
	client.Timeout = musicAudioImportTimeout
	requestCtx, cancel := context.WithTimeout(ctx, musicAudioImportTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.endpoint.String()+path, bytes.NewReader(audio))
	if err != nil {
		return MusicAudioImportJobStatus{}, errors.New("build Music audio import request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "audio/flac")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-RenCrow-Music-Expected-Actor", c.actorID)
	req.Header.Set("X-RenCrow-Music-Project-ID", projectID)
	req.Header.Set("X-Source-Module", "core_attachment")
	req.Header.Set("X-Source-ID", sourceAttachmentID)
	req.Header.Set("X-Expected-SHA256", expectedSHA256)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.GetBody = nil
	response, err := client.Do(req)
	if err != nil {
		return MusicAudioImportJobStatus{}, fmt.Errorf("music audio import request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return MusicAudioImportJobStatus{}, musicGatewayStatusError(response)
	}
	var status MusicAudioImportJobStatus
	if err := decodeMusicJSONResponse(response, maxMusicGatewayResponse, false, &status); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if err := validateMusicAudioImportJobStatus(status); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if status.ProjectID != projectID || status.SourceID != sourceAttachmentID || status.InputSHA256 != expectedSHA256 {
		return MusicAudioImportJobStatus{}, errors.New("Music audio import response does not match the request")
	}
	return status, nil
}

func (c *MusicProjectGatewayClient) GetMusicAudioImportJob(ctx context.Context, jobID string) (MusicAudioImportJobStatus, error) {
	if err := validateMusicJobID(jobID); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	var status MusicAudioImportJobStatus
	path := "/v1/music/jobs/" + url.PathEscape(jobID)
	if err := c.doJSONWithPolicy(ctx, c.client, http.MethodGet, path, "", nil, "application/json", "", []int{http.StatusOK}, maxMusicGatewayResponse, false, &status); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if err := validateMusicAudioImportJobStatus(status); err != nil {
		return MusicAudioImportJobStatus{}, err
	}
	if status.JobID != jobID {
		return MusicAudioImportJobStatus{}, errors.New("Music audio import response ID does not match the request")
	}
	return status, nil
}

func validateMusicAnalysis(analysis MusicAnalysis) error {
	if validateMusicOpaqueID(analysis.AnalysisID, "music analysis ID") != nil || validateMusicProjectID(analysis.ProjectID) != nil ||
		validateMusicArtifactID(analysis.SourceArtifactID) != nil || !musicArtifactSHA256Pattern.MatchString(analysis.SourceSHA256) ||
		(analysis.Operation != "beats" && analysis.Operation != "captions") ||
		(analysis.State != "succeeded" && analysis.State != "running" && analysis.State != "failed" && analysis.State != "needs_reconciliation") ||
		analysis.CreatedAt.IsZero() || analysis.UpdatedAt.IsZero() || analysis.FailureCode != "" && !musicFailureCodePattern.MatchString(analysis.FailureCode) {
		return errors.New("music analysis response does not match the supported schema")
	}
	if analysis.State != "succeeded" {
		if len(analysis.Result) != 0 || analysis.ResultSHA256 != "" {
			return errors.New("non-successful music analysis unexpectedly contains a result")
		}
		if analysis.State == "running" && analysis.FailureCode != "" ||
			(analysis.State == "failed" || analysis.State == "needs_reconciliation") && analysis.FailureCode == "" {
			return errors.New("music analysis state and failure metadata are inconsistent")
		}
		return nil
	}
	if analysis.FailureCode != "" || len(analysis.Result) == 0 || len(analysis.Result) > maxMusicAnalysisResult || !musicArtifactSHA256Pattern.MatchString(analysis.ResultSHA256) {
		return errors.New("successful music analysis is missing a bounded result")
	}
	resultHash := sha256.Sum256(analysis.Result)
	if hex.EncodeToString(resultHash[:]) != analysis.ResultSHA256 {
		return errors.New("music analysis result checksum does not match its document")
	}
	decoder := json.NewDecoder(bytes.NewReader(analysis.Result))
	decoder.DisallowUnknownFields()
	var document MusicAnalysisDocument
	if err := decoder.Decode(&document); err != nil {
		return errors.New("music analysis result document is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("music analysis result contains trailing JSON")
	}
	if document.SchemaVersion != "rencrow.music-analysis.v1" || document.Operation != analysis.Operation || document.SourceSHA256 != analysis.SourceSHA256 ||
		document.SampleRate < 8000 || document.SampleRate > 384000 || document.DurationFrames < 1 || document.DurationFrames > int64(document.SampleRate)*180 ||
		document.Beats == nil || document.Words == nil || (document.BPM != nil && (math.IsNaN(*document.BPM) || math.IsInf(*document.BPM, 0) || *document.BPM <= 0 || *document.BPM > 600)) {
		return errors.New("music analysis result metadata is inconsistent")
	}
	if document.Versions.Worker != "rencrow.music-analysis-python.v1" || document.Versions.Librosa != "0.11.0" || !validMusicText(document.Versions.Python, 80) ||
		analysis.Operation == "beats" && len(document.Words) != 0 ||
		analysis.Operation == "captions" && (len(document.Beats) != 0 || document.BPM != nil || document.Versions.WhisperX != "3.8.6") || analysis.FailureCode != "" {
		return errors.New("music analysis result contains data for the other operation")
	}
	for i, beat := range document.Beats {
		if beat.Frame < 0 || beat.Frame >= document.DurationFrames || math.IsNaN(beat.TimeSeconds) || math.IsInf(beat.TimeSeconds, 0) || beat.TimeSeconds < 0 || beat.TimeSeconds > 180 || (i > 0 && beat.Frame <= document.Beats[i-1].Frame) {
			return errors.New("music analysis beat data is outside its source timeline")
		}
	}
	for _, word := range document.Words {
		if strings.TrimSpace(word.Text) == "" || len([]rune(word.Text)) > 4000 || word.StartFrame < 0 || word.EndFrame <= word.StartFrame || word.EndFrame > document.DurationFrames {
			return errors.New("music analysis caption data is outside its source timeline")
		}
	}
	return nil
}

func validateMusicSourceSearchResponse(response MusicSourceSearchResponse, expectedPage, expectedPageSize int) error {
	if response.Page != expectedPage || response.PageSize != expectedPageSize || response.Total < len(response.Results) || len(response.Results) > expectedPageSize {
		return errors.New("music source search response does not match the requested page")
	}
	for _, result := range response.Results {
		if !validMusicText(result.ID, 256) || !validMusicText(result.Title, 1000) || !validMusicText(result.Provider, 200) ||
			!validMusicText(result.Source, 200) || !validMusicText(result.License, 200) || len(result.LicenseVersion) > 100 ||
			len(result.Attribution) > 4000 || len(result.ForeignLandingURL) > 2048 || len(result.LicenseURL) > 2048 ||
			!validExternalMusicURL(result.ForeignLandingURL) || !validExternalMusicURL(result.LicenseURL) ||
			(result.Duration != nil && (math.IsNaN(*result.Duration) || math.IsInf(*result.Duration, 0) || *result.Duration < 0 || *result.Duration > 86400)) {
			return errors.New("music source search response contains invalid metadata")
		}
	}
	return nil
}

func validExternalMusicURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && len(parsed.String()) <= 2048
}

func validateMusicAudioImportJobStatus(status MusicAudioImportJobStatus) error {
	if validateMusicJobID(status.JobID) != nil || status.Kind != "audio_import" || validateMusicProjectID(status.ProjectID) != nil ||
		(status.State != "succeeded" && status.State != "failed" && status.State != "needs_reconciliation") ||
		!musicArtifactSHA256Pattern.MatchString(status.InputSHA256) || status.SourceModule != "core_attachment" || validateMusicOpaqueID(status.SourceID, "source attachment ID") != nil ||
		status.CreatedAt.IsZero() || status.UpdatedAt.IsZero() || status.FailureCode != "" && !musicFailureCodePattern.MatchString(status.FailureCode) {
		return errors.New("Music audio import status does not match the supported schema")
	}
	if status.State != "succeeded" {
		if status.ArtifactID != "" || status.SizeBytes != 0 || status.MediaType != "" || status.Format != "" || status.DurationFrames != 0 ||
			status.SampleRate != 0 || status.Channels != 0 || status.DurationActualS != 0 || status.FailureCode == "" {
			return errors.New("unsuccessful Music audio import status unexpectedly contains artifact metadata")
		}
		return nil
	}
	if validateMusicArtifactID(status.ArtifactID) != nil || status.SizeBytes < 4 || status.SizeBytes > maxMusicAudioImportBytes || status.MediaType != "audio/flac" || status.Format != "flac" ||
		status.DurationFrames < 1 || status.SampleRate < 8000 || status.SampleRate > 384000 || status.Channels < 1 || status.Channels > 8 || status.DurationFrames > int64(status.SampleRate)*180 ||
		math.IsNaN(status.DurationActualS) || math.IsInf(status.DurationActualS, 0) || status.DurationActualS <= 0 || status.DurationActualS > 180 ||
		math.Abs(status.DurationActualS-float64(status.DurationFrames)/float64(status.SampleRate)) > 0.000001 || status.FailureCode != "" {
		return errors.New("successful Music audio import status has invalid native audio metadata")
	}
	return nil
}

func decodeMusicJSONResponse(response *http.Response, maxBytes int64, allowUnknown bool, target any) error {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("music gateway response is not JSON")
	}
	if response.ContentLength > maxBytes {
		return errors.New("music gateway response exceeds the configured limit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return errors.New("read Music audio import response")
	}
	if int64(len(data)) > maxBytes {
		return errors.New("music gateway response exceeds the configured limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !allowUnknown {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return errors.New("Music audio import response is invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("music gateway response contains trailing JSON")
	}
	return nil
}

func validateMusicOpaqueID(value, description string) error {
	if !validMusicOpaqueID(value) {
		return fmt.Errorf("%s is invalid", description)
	}
	return nil
}
