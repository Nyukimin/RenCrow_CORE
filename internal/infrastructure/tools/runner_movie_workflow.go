package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) movieMusicTimingIntegrationReady() bool {
	return r.config.Movie != nil && r.config.MovieTimingReady && r.config.MusicArtifactMetadata != nil &&
		r.config.Movie.ActorID() == r.config.MusicArtifactMetadata.ActorID() && r.movieMusicAnalysisReader() != nil
}

func (r *ToolRunner) movieMusicAnalysisReader() MusicAnalysisGateway {
	if r.config.Movie == nil || r.config.MusicAnalysisReader == nil {
		return nil
	}
	if r.config.MusicAnalysisReader.ActorID() == r.config.Movie.ActorID() {
		return r.config.MusicAnalysisReader
	}
	return nil
}

type MovieRenderJobResult struct {
	Job         MovieRenderJob `json:"job"`
	DownloadURL string         `json:"download_url,omitempty"`
}

func movieAssetImportMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "movie.asset.import", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "設定済みMusic actorが所有するopaque audio artifactを取得し、hashとprovenanceを検証してRenCrow_Movieへimportする。任意path/URL/base64は受け付けない。",
		Invariants: []string{
			"Music artifactは同じ設定済みactorが所有し、Music APIからbytesとmetadataを取得してhashを検証する",
			"Movie source_moduleはmusic、source_idはMusic artifact IDに固定する",
			"idempotency keyは認証済みrequest IDからCOREが導出する",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"music_artifact_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
			"required": []any{"music_artifact_id"},
		},
	}
}

func movieRenderSubmitMetadata(allowAnalysis bool) tool.ToolMetadata {
	properties := map[string]any{
		"schema_version":  map[string]any{"type": "integer", "const": 1},
		"width":           map[string]any{"type": "integer", "minimum": 16, "maximum": 3840, "multipleOf": 2},
		"height":          map[string]any{"type": "integer", "minimum": 16, "maximum": 2160, "multipleOf": 2},
		"fps":             map[string]any{"type": "integer", "minimum": 1, "maximum": 60},
		"duration_frames": map[string]any{"type": "integer", "minimum": 1, "maximum": 10800, "description": "Required unless Movie timing is derived from music_analysis_ids."},
		"title":           map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"scenes":          movieSceneArraySchema(allowAnalysis),
		"beat_frames":     map[string]any{"type": "array", "maxItems": 5000, "items": map[string]any{"type": "integer", "minimum": 0}},
		"captions":        movieCaptionArraySchema(),
		"audio_asset_id":  map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
	}
	planSchema := map[string]any{
		"type": "object", "additionalProperties": false, "properties": properties,
		"required": []any{"schema_version", "width", "height", "fps", "title", "scenes"},
	}
	if !allowAnalysis {
		planSchema["required"] = []any{"schema_version", "width", "height", "fps", "duration_frames", "title", "scenes"}
	}
	outerProperties := map[string]any{"plan": planSchema}
	required := []any{"plan"}
	if allowAnalysis {
		outerProperties["music_analysis_ids"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}}
	}
	return tool.ToolMetadata{
		ToolID: "movie.render.submit", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "検証済みpublic Movie planをRenCrow_Movieへ一度だけ送信する。利用可能ならMusic analysis IDsからMovie自身のtiming receiptを取得し、返却frame配列だけをplanへ反映する。",
		Invariants: []string{
			"Music analysisが指定された場合はimport済みMovie audio assetのprovenance/hashと一致を確認する",
			"frame変換はMovie timing APIだけが行い、COREは返却配列を合成してframe arithmeticを行わない",
			"request timeout後は同じidempotency keyで状態確認し、別keyで再送しない",
		},
		Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": outerProperties, "required": required},
	}
}

func movieSceneArraySchema(allowMissingDuration bool) map[string]any {
	required := []any{"kind", "start_frame", "duration_frames"}
	if allowMissingDuration {
		required = []any{"kind", "start_frame"}
	}
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": 120,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"kind":            map[string]any{"type": "string", "enum": []any{"title", "image", "video", "lottie", "three"}},
				"start_frame":     map[string]any{"type": "integer", "minimum": 0, "maximum": 10800},
				"duration_frames": map[string]any{"type": "integer", "minimum": 1, "maximum": 10800},
				"asset_id":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"text":            map[string]any{"type": "string", "maxLength": 4000},
			},
			"required": required,
		},
	}
}

func movieCaptionArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 500,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"start_frame": map[string]any{"type": "integer", "minimum": 0},
				"end_frame":   map[string]any{"type": "integer", "minimum": 1},
				"text":        map[string]any{"type": "string", "minLength": 1, "maxLength": 4000},
			},
			"required": []any{"start_frame", "end_frame", "text"},
		},
	}
}

func movieRenderStatusMetadata(includeDownload bool) tool.ToolMetadata {
	properties := map[string]any{
		"job_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 30, "default": 0, "description": "Bounded synchronous status wait; zero returns current state."},
	}
	description := "取得actor-owned Movie render jobの最新状態。"
	if includeDownload {
		description += "成功時のみ既存CMD owner-authenticated download linkを返す。"
	}
	return tool.ToolMetadata{
		ToolID: "movie.render.status", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: description,
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false, "properties": properties,
			"required": []any{"job_id"},
		},
	}
}

func (r *ToolRunner) executeMovieAssetImportV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	artifactID, err := normalizeMovieAssetImportArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "movie.asset.import arguments are invalid", nil), nil
	}
	if r.config.Movie == nil || !r.config.MovieAssetStorageReady || r.config.MusicArtifactMetadata == nil || r.config.MusicArtifactPlayback == nil || r.config.Movie.ActorID() != r.config.MusicArtifactMetadata.ActorID() || r.config.Movie.ActorID() != r.config.MusicArtifactPlayback.ActorID() {
		return tool.NewError(tool.ErrNotFound, "Music-to-Movie asset import is unavailable", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.Movie.ActorID())
	if failure != nil {
		return failure, nil
	}
	artifact, err := r.config.MusicArtifactMetadata.GetMusicArtifact(ctx, artifactID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	if artifact.Stale || artifact.SizeBytes < 1 || artifact.SizeBytes > maxMovieAssetBytes || artifact.MediaType != "audio/flac" && artifact.MediaType != "audio/wav" && artifact.MediaType != "audio/x-wav" {
		return tool.NewError(tool.ErrValidationFailed, "Music artifact is not a current supported audio asset", nil), nil
	}
	response, err := r.config.MusicArtifactPlayback.OpenMusicArtifact(ctx, artifact.ID, "")
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	if response == nil || response.Body == nil {
		return tool.NewError(tool.ErrInternalError, "Music artifact bytes are unavailable", nil), nil
	}
	defer response.Body.Close()
	mediaType, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || mediaType != artifact.MediaType || response.ContentLength != artifact.SizeBytes || response.ContentLength < 1 || response.ContentLength > maxMovieAssetBytes || response.Header.Get("ETag") != `"sha256:`+artifact.SHA256+`"` {
		return tool.NewError(tool.ErrInternalError, "Music artifact response failed its metadata checks", nil), nil
	}
	audio, err := io.ReadAll(io.LimitReader(response.Body, maxMovieAssetBytes+1))
	if err != nil || int64(len(audio)) != artifact.SizeBytes || int64(len(audio)) > maxMovieAssetBytes {
		return tool.NewError(tool.ErrInternalError, "Music artifact stream exceeded its verified size", nil), nil
	}
	hash := sha256.Sum256(audio)
	if hex.EncodeToString(hash[:]) != artifact.SHA256 {
		return tool.NewError(tool.ErrInternalError, "Music artifact checksum does not match its metadata", nil), nil
	}
	key, ok := trustedToolActionIdempotencyKey(ctx, "rencrow-core-movie-asset-import-", proof)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Movie asset contract", nil), nil
	}
	asset, err := r.config.Movie.CreateMovieAsset(ctx, artifact.MediaType, "music", artifact.ID, artifact.SHA256, audio, key)
	if err != nil {
		return movieGatewayToolError(err), nil
	}
	return tool.NewSuccess(asset), nil
}

func (r *ToolRunner) executeMovieRenderSubmitV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	plan, analysisIDs, err := normalizeMovieRenderSubmitArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "movie.render.submit arguments are invalid", nil), nil
	}
	if r.config.Movie == nil || !r.config.MovieRenderReady {
		return tool.NewError(tool.ErrNotFound, "Movie render submission is unavailable", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.Movie.ActorID())
	if failure != nil {
		return failure, nil
	}
	if err := validateMovieRenderPlan(plan, len(analysisIDs) > 0); err != nil {
		return tool.NewError(tool.ErrValidationFailed, "movie.render.submit plan is invalid", nil), nil
	}
	if len(analysisIDs) > 0 {
		if !r.movieMusicTimingIntegrationReady() {
			return tool.NewError(tool.ErrNotFound, "Movie music timing integration is unavailable", nil), nil
		}
		if err := r.composeMovieMusicTiming(ctx, &plan, analysisIDs); err != nil {
			return musicOrMovieToolError(err), nil
		}
	} else if plan.DurationFrames == nil {
		return tool.NewError(tool.ErrValidationFailed, "Movie duration is required without Music timing", nil), nil
	}
	if plan.AudioAssetID != "" {
		asset, err := r.config.Movie.GetMovieAssetMetadata(ctx, plan.AudioAssetID)
		if err != nil {
			return movieGatewayToolError(err), nil
		}
		if !strings.HasPrefix(asset.MediaType, "audio/") {
			return tool.NewError(tool.ErrValidationFailed, "Movie audio_asset_id does not reference an audio asset", nil), nil
		}
		if len(analysisIDs) > 0 {
			analysisGateway := r.movieMusicAnalysisReader()
			if analysisGateway == nil {
				return tool.NewError(tool.ErrNotFound, "Movie music timing integration is unavailable", nil), nil
			}
			analysis, err := analysisGateway.GetMusicAnalysis(ctx, analysisIDs[0])
			if err != nil {
				return musicGatewayToolError(err), nil
			}
			if asset.SourceModule != "music" || asset.SourceID != analysis.SourceArtifactID || asset.SHA256 != analysis.SourceSHA256 {
				return tool.NewError(tool.ErrValidationFailed, "Movie audio asset provenance does not match the Music analysis source", nil), nil
			}
		}
	} else if len(analysisIDs) > 0 {
		return tool.NewError(tool.ErrValidationFailed, "audio_asset_id is required when Music analysis timing is used", nil), nil
	}
	if err := validateMovieRenderPlan(plan, false); err != nil {
		return tool.NewError(tool.ErrValidationFailed, "movie.render.submit plan is missing complete timings", nil), nil
	}
	key, ok := trustedToolActionIdempotencyKey(ctx, "rencrow-core-movie-render-", proof)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Movie render contract", nil), nil
	}
	job, err := r.config.Movie.SubmitMovieRender(ctx, plan, key)
	if err != nil {
		return movieGatewayToolError(err), nil
	}
	return tool.NewSuccess(movieRenderJobResult(job, r.config.MoviePlaybackLinkReady)), nil
}

func (r *ToolRunner) executeMovieRenderStatusV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	jobID, waitSeconds, err := normalizeMovieRenderStatusArgs(args, true)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "movie.render.status arguments are invalid", nil), nil
	}
	if r.config.Movie == nil {
		return tool.NewError(tool.ErrNotFound, "Movie render status is unavailable", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.Movie.ActorID()); failure != nil {
		return failure, nil
	}
	job, err := r.config.Movie.GetMovieRenderJob(ctx, jobID)
	if err != nil {
		return movieGatewayToolError(err), nil
	}
	if waitSeconds > 0 && !movieRenderTerminal(job.State) {
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(waitSeconds)*time.Second)
		defer cancel()
		deadline := time.NewTimer(time.Duration(waitSeconds) * time.Second)
		defer deadline.Stop()
		for {
			pause := time.NewTimer(500 * time.Millisecond)
			select {
			case <-waitCtx.Done():
				pause.Stop()
				return tool.NewSuccess(movieRenderJobResult(job, r.config.MoviePlaybackLinkReady)), nil
			case <-deadline.C:
				pause.Stop()
				return tool.NewSuccess(movieRenderJobResult(job, r.config.MoviePlaybackLinkReady)), nil
			case <-pause.C:
			}
			updated, pollErr := r.config.Movie.GetMovieRenderJob(waitCtx, jobID)
			if pollErr != nil {
				if errors.Is(pollErr, context.DeadlineExceeded) || errors.Is(pollErr, context.Canceled) && ctx.Err() == nil {
					return tool.NewSuccess(movieRenderJobResult(job, r.config.MoviePlaybackLinkReady)), nil
				}
				return movieGatewayToolError(pollErr), nil
			}
			job = updated
			if movieRenderTerminal(job.State) {
				break
			}
		}
	}
	return tool.NewSuccess(movieRenderJobResult(job, r.config.MoviePlaybackLinkReady)), nil
}

func movieRenderJobResult(job MovieRenderJob, includeDownload bool) MovieRenderJobResult {
	result := MovieRenderJobResult{Job: job}
	if includeDownload && job.State == "succeeded" {
		result.DownloadURL = movieRenderDownloadPath(job.ID)
	}
	return result
}

func movieRenderDownloadPath(jobID string) string {
	return "/viewer/movie/jobs/" + url.PathEscape(jobID) + "/download?to=mio"
}

func movieRenderTerminal(state string) bool {
	return state == "succeeded" || state == "failed" || state == "needs_reconciliation"
}

func (r *ToolRunner) composeMovieMusicTiming(ctx context.Context, plan *MovieRenderPlan, analysisIDs []string) error {
	if plan.AudioAssetID == "" {
		return errors.New("Movie render plan with Music analyses requires audio_asset_id")
	}
	analysisGateway := r.movieMusicAnalysisReader()
	if analysisGateway == nil {
		return errors.New("Music analysis gateway is unavailable")
	}
	asset, err := r.config.Movie.GetMovieAssetMetadata(ctx, plan.AudioAssetID)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(asset.MediaType, "audio/") {
		return errors.New("Movie audio asset metadata is not audio")
	}
	var sourceArtifact MusicArtifact
	var baseDocument MusicAnalysisDocument
	seenOperation := make(map[string]struct{}, len(analysisIDs))
	type operationTiming struct {
		operation string
		receipt   MovieTimingReceipt
	}
	receipts := make([]operationTiming, 0, len(analysisIDs))
	for _, analysisID := range analysisIDs {
		analysis, err := analysisGateway.GetMusicAnalysis(ctx, analysisID)
		if err != nil {
			return err
		}
		if analysis.State != "succeeded" {
			return errors.New("Music analysis has not succeeded")
		}
		if _, exists := seenOperation[analysis.Operation]; exists {
			return errors.New("Music analysis IDs must have distinct operations")
		}
		seenOperation[analysis.Operation] = struct{}{}
		artifact, err := r.config.MusicArtifactMetadata.GetMusicArtifact(ctx, analysis.SourceArtifactID)
		if err != nil {
			return err
		}
		if artifact.ProjectID != analysis.ProjectID || artifact.SHA256 != analysis.SourceSHA256 || artifact.Stale || !strings.HasPrefix(artifact.MediaType, "audio/") {
			return errors.New("Music analysis source artifact ownership or checksum does not match")
		}
		var document MusicAnalysisDocument
		decoder := json.NewDecoder(bytes.NewReader(analysis.Result))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			return errors.New("Music analysis result document is invalid")
		}
		if artifact.SampleRate != document.SampleRate || artifact.DurationFrames != document.DurationFrames {
			return errors.New("Music analysis timeline does not match the owned source artifact metadata")
		}
		if len(receipts) == 0 {
			sourceArtifact = artifact
			baseDocument = document
		} else if artifact.ID != sourceArtifact.ID || artifact.SHA256 != sourceArtifact.SHA256 || document.SampleRate != baseDocument.SampleRate || document.DurationFrames != baseDocument.DurationFrames {
			return errors.New("Music analysis results do not share the same source audio and timeline")
		}
		if asset.SourceModule != "music" || asset.SourceID != artifact.ID || asset.SHA256 != artifact.SHA256 {
			return errors.New("Movie audio asset provenance does not match the Music analysis source")
		}
		receipt, err := r.config.Movie.CreateMovieTimings(ctx, analysis, plan.FPS)
		if err != nil {
			return err
		}
		receipts = append(receipts, operationTiming{operation: analysis.Operation, receipt: receipt})
	}
	base := receipts[0].receipt
	for _, timing := range receipts[1:] {
		receipt := timing.receipt
		if receipt.FPS != base.FPS || receipt.SourceSampleRate != base.SourceSampleRate || receipt.SourceDurationFrames != base.SourceDurationFrames || receipt.VideoDurationFrames != base.VideoDurationFrames {
			return errors.New("Movie timing receipts disagree on their shared timeline")
		}
	}
	for _, timing := range receipts {
		receipt := timing.receipt
		if timing.operation == "beats" {
			if plan.BeatFrames != nil && !reflect.DeepEqual(plan.BeatFrames, receipt.BeatFrames) {
				return errors.New("plan beat_frames conflict with the Movie timing receipt")
			}
			plan.BeatFrames = append([]int64(nil), receipt.BeatFrames...)
		}
		if timing.operation == "captions" {
			if plan.Captions != nil && !reflect.DeepEqual(plan.Captions, receipt.Captions) {
				return errors.New("plan captions conflict with the Movie timing receipt")
			}
			plan.Captions = append([]MovieCaption(nil), receipt.Captions...)
		}
	}
	if plan.DurationFrames == nil {
		duration := int(base.VideoDurationFrames)
		plan.DurationFrames = &duration
	} else if int64(*plan.DurationFrames) != base.VideoDurationFrames {
		return errors.New("plan duration_frames conflicts with the Movie timing receipt")
	}
	if len(plan.Scenes) == 1 && plan.Scenes[0].StartFrame == 0 && plan.Scenes[0].DurationFrames == nil {
		duration := int(base.VideoDurationFrames)
		plan.Scenes[0].DurationFrames = &duration
	}
	return validateMovieRenderPlan(*plan, false)
}

func normalizeMovieAssetImportArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "music_artifact_id" {
			return "", errors.New("unsupported argument")
		}
	}
	artifactID, ok := args["music_artifact_id"].(string)
	if !ok || validateMusicArtifactID(artifactID) != nil {
		return "", errors.New("Music artifact ID is invalid")
	}
	return artifactID, nil
}

func normalizeMovieRenderSubmitArgs(args map[string]any) (MovieRenderPlan, []string, error) {
	for key := range args {
		if key != "plan" && key != "music_analysis_ids" {
			return MovieRenderPlan{}, nil, errors.New("unsupported argument")
		}
	}
	planInput, exists := args["plan"]
	if !exists || planInput == nil {
		return MovieRenderPlan{}, nil, errors.New("plan is required")
	}
	encoded, err := json.Marshal(planInput)
	if err != nil || len(encoded) > maxMovieRenderRequest {
		return MovieRenderPlan{}, nil, errors.New("plan is invalid or exceeds the supported size")
	}
	if err := rejectNullMovieTimingFields(encoded); err != nil {
		return MovieRenderPlan{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var plan MovieRenderPlan
	if err := decoder.Decode(&plan); err != nil {
		return MovieRenderPlan{}, nil, errors.New("plan contains unsupported fields or invalid values")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return MovieRenderPlan{}, nil, errors.New("plan contains trailing data")
	}
	var analysisIDs []string
	if raw, exists := args["music_analysis_ids"]; exists {
		values, ok := raw.([]any)
		if !ok || len(values) < 1 || len(values) > 2 {
			return MovieRenderPlan{}, nil, errors.New("music_analysis_ids must contain one or two IDs")
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			id, ok := value.(string)
			if !ok || validateMusicOpaqueID(id, "Music analysis ID") != nil {
				return MovieRenderPlan{}, nil, errors.New("music_analysis_ids contains an invalid ID")
			}
			if _, exists := seen[id]; exists {
				return MovieRenderPlan{}, nil, errors.New("music_analysis_ids must be unique")
			}
			seen[id] = struct{}{}
			analysisIDs = append(analysisIDs, id)
		}
	}
	return plan, analysisIDs, nil
}

func rejectNullMovieTimingFields(encoded []byte) error {
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return errors.New("plan is invalid JSON")
	}
	if movieJSONContainsNull(value) {
		return errors.New("plan fields must be omitted or concrete values, not null")
	}
	return nil
}

func movieJSONContainsNull(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range typed {
			if movieJSONContainsNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if movieJSONContainsNull(child) {
				return true
			}
		}
	}
	return false
}

func normalizeMovieRenderStatusArgs(args map[string]any, includeWait bool) (string, int, error) {
	for key := range args {
		if key != "job_id" && !(includeWait && key == "wait_seconds") {
			return "", 0, errors.New("unsupported argument")
		}
	}
	jobID, ok := args["job_id"].(string)
	if !ok || !movieAssetIDPattern.MatchString(jobID) {
		return "", 0, errors.New("job ID is invalid")
	}
	waitSeconds := 0
	if raw, exists := args["wait_seconds"]; exists {
		var valid bool
		waitSeconds, valid = integerToolArgument(raw)
		if !valid || waitSeconds < 0 || waitSeconds > 30 {
			return "", 0, errors.New("wait_seconds must be between zero and thirty")
		}
	}
	return jobID, waitSeconds, nil
}

func movieGatewayToolError(err error) *tool.ToolResponse {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return tool.NewError(tool.ErrTimeout, "Movie gateway request timed out", nil)
	}
	var statusErr *MovieGatewayStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			return tool.NewError(tool.ErrValidationFailed, "Movie gateway rejected the request", nil)
		case http.StatusUnauthorized, http.StatusForbidden:
			return tool.NewError(tool.ErrPermissionDenied, "Movie gateway rejected its configured credential", nil)
		case http.StatusNotFound:
			return tool.NewError(tool.ErrNotFound, "Movie resource was not found for this actor", nil)
		case http.StatusConflict:
			return tool.NewError(tool.ErrConflict, "Movie idempotency key conflicts with an earlier request", nil)
		case http.StatusTooManyRequests:
			return tool.NewError(tool.ErrRateLimited, "Movie gateway is temporarily unavailable", nil)
		case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusInsufficientStorage:
			return tool.NewError(tool.ErrInternalError, "Movie gateway is temporarily unavailable", nil)
		}
	}
	return tool.NewError(tool.ErrInternalError, "Movie gateway request failed", nil)
}

func musicOrMovieToolError(err error) *tool.ToolResponse {
	var musicStatus *MusicGatewayStatusError
	if errors.As(err, &musicStatus) {
		return musicGatewayToolError(err)
	}
	var movieStatus *MovieGatewayStatusError
	if errors.As(err, &movieStatus) {
		return movieGatewayToolError(err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return tool.NewError(tool.ErrTimeout, "Music or Movie gateway request timed out", nil)
	}
	return tool.NewError(tool.ErrValidationFailed, "Music analysis and Movie timing could not be composed", nil)
}
