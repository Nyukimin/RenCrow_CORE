package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const musicEditTimelineFrames = 20 * 48000
const musicEditMaxFadeFrames = 24000

var musicFailureCodePattern = regexp.MustCompile(`^[A-Z0-9_.-]{1,64}$`)

func (r *ToolRunner) registerMusicGenerationTools() {
	r.toolsV2["music.generation.submit"] = r.executeMusicGenerationSubmitV2
	r.toolsV2["music.generation.status"] = r.executeMusicGenerationStatusV2
}

func (r *ToolRunner) registerMusicRemixTools() {
	r.toolsV2["music.remix.create"] = r.executeMusicRemixCreateV2
}

func musicGenerationSubmitMetadata(requireRequestID bool) tool.ToolMetadata {
	properties := map[string]any{
		"project_id":         map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"plan_id":            map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"expected_plan_hash": map[string]any{"type": "string", "pattern": `^sha256:[0-9a-f]{64}$`},
	}
	required := []any{"project_id", "plan_id", "expected_plan_hash"}
	description := "RenCrow_Musicで検証済みproduction planの生成jobを一度だけ受け付ける。生成先やbackend routeは指定できない。"
	if requireRequestID {
		properties["request_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
		required = append(required, "request_id")
		description += " music.request.createで保存した正規化requestを先に作り、その返却request_idを渡す。"
	}
	return tool.ToolMetadata{
		ToolID: "music.generation.submit", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: description,
		Invariants: []string{
			"project actorは設定値と認証済みHTTP利用者に固定され、Tool引数で変更できない",
			"idempotency keyは認証済みrequest IDからCOREが導出する",
			"plan IDとplan hashを固定し、生成backendやStudioの指定を受け付けない",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties,
			"required":   required,
		},
	}
}

func musicGenerationStatusMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.generation.status", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicから設定済みactorが所有するgeneration jobの状態を取得する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"job_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
			"required":   []any{"job_id"},
		},
	}
}

func musicRemixCreateMetadata(requireRequestID bool) tool.ToolMetadata {
	properties := map[string]any{
		"project_id":         map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"source_artifact_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
		"start_frame":        map[string]any{"type": "integer", "minimum": 0, "maximum": musicEditTimelineFrames - 1},
		"end_frame":          map[string]any{"type": "integer", "minimum": 1, "maximum": musicEditTimelineFrames},
		"gain_db":            map[string]any{"type": "number", "minimum": -12, "maximum": 12},
		"fade_frames":        map[string]any{"type": "integer", "minimum": 0, "maximum": musicEditMaxFadeFrames},
	}
	required := []any{"project_id", "source_artifact_id", "start_frame", "end_frame", "gain_db", "fade_frames"}
	description := "同一project内の音声artifactに対して20秒以内のcrop、gain、fadeを適用し、新しいartifactを作る。"
	if requireRequestID {
		properties["request_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
		required = append(required, "request_id")
		description += " music.request.createで保存した正規化requestを先に作り、その返却request_idを渡す。"
	}
	return tool.ToolMetadata{
		ToolID: "music.remix.create", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: description,
		Invariants: []string{
			"source artifactは同一設定actor・project所有であることをMusic側でも確認する",
			"時刻は48 kHz timelineのhalf-open frame範囲で指定し、元artifactを変更しない",
			"gainは-12..12 dB、fadeは最大24000 framesで、左右fadeを選択範囲内に収める",
			"idempotency keyは認証済みrequest IDからCOREが導出する",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": properties,
			"required":   required,
		},
	}
}

func (r *ToolRunner) executeMusicGenerationSubmitV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicGeneration == nil {
		return tool.NewError(tool.ErrNotFound, "Music generation is unavailable", nil), nil
	}
	projectID, input, err := normalizeMusicGenerationSubmitArgs(args, r.config.MusicControl != nil)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.generation.submit arguments are invalid", nil), nil
	}
	if r.config.MusicControl != nil && input.RequestID == "" {
		return tool.NewError(tool.ErrValidationFailed, "music.generation.submit requires a saved Music request ID", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicGeneration.ActorID())
	if failure != nil {
		return failure, nil
	}
	key, ok := musicProofIdempotencyKey("rencrow-core-generation-", proof.RequestID)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music generation contract", nil), nil
	}
	accepted, err := r.config.MusicGeneration.SubmitMusicGeneration(ctx, projectID, input, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(accepted), nil
}

func (r *ToolRunner) executeMusicGenerationStatusV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicGeneration == nil {
		return tool.NewError(tool.ErrNotFound, "Music generation is unavailable", nil), nil
	}
	jobID, err := normalizeMusicGenerationStatusArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.generation.status arguments are invalid", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicGeneration.ActorID()); failure != nil {
		return failure, nil
	}
	job, err := r.config.MusicGeneration.GetMusicJob(ctx, jobID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	result := MusicGenerationJobResult{MusicJobStatus: job}
	if r.config.MusicPlaybackLinkReady && r.config.MusicArtifactPlayback != nil && job.ArtifactID != "" {
		result.PlaybackURL = musicArtifactPlaybackPath(job.ArtifactID)
	}
	return tool.NewSuccess(result), nil
}

func (r *ToolRunner) executeMusicRemixCreateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicRemix == nil {
		return tool.NewError(tool.ErrNotFound, "Music remix is unavailable", nil), nil
	}
	projectID, input, err := normalizeMusicEditArgs(args, r.config.MusicControl != nil)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.remix.create arguments are invalid", nil), nil
	}
	if r.config.MusicControl != nil && input.RequestID == "" {
		return tool.NewError(tool.ErrValidationFailed, "music.remix.create requires a saved Music request ID", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicRemix.ActorID())
	if failure != nil {
		return failure, nil
	}
	key, ok := musicProofIdempotencyKey("rencrow-core-remix-", proof.RequestID)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music remix contract", nil), nil
	}
	artifact, err := r.config.MusicRemix.CreateMusicEdit(ctx, projectID, input, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	result := MusicArtifactResult{MusicArtifact: artifact}
	if r.config.MusicPlaybackLinkReady && r.config.MusicArtifactPlayback != nil {
		result.PlaybackURL = musicArtifactPlaybackPath(artifact.ID)
	}
	return tool.NewSuccess(result), nil
}

func normalizeMusicGenerationSubmitArgs(args map[string]any, allowRequestID bool) (string, MusicGenerationRequest, error) {
	for key := range args {
		if key != "project_id" && key != "plan_id" && key != "expected_plan_hash" && !(allowRequestID && key == "request_id") {
			return "", MusicGenerationRequest{}, errors.New("unsupported generation submit argument")
		}
	}
	projectID, projectOK := args["project_id"].(string)
	planID, planOK := args["plan_id"].(string)
	hash, hashOK := args["expected_plan_hash"].(string)
	if !projectOK || validateMusicProjectID(projectID) != nil || !planOK || validateMusicPlanID(planID) != nil || !hashOK || !musicPlanHashPattern.MatchString(hash) {
		return "", MusicGenerationRequest{}, errors.New("generation project, plan, or hash is invalid")
	}
	requestID := ""
	if raw, exists := args["request_id"]; exists {
		var ok bool
		requestID, ok = raw.(string)
		if !ok || validateMusicRequestID(requestID) != nil {
			return "", MusicGenerationRequest{}, errors.New("request ID is invalid")
		}
	}
	return projectID, MusicGenerationRequest{PlanID: planID, ExpectedPlanHash: hash, RequestID: requestID}, nil
}

func normalizeMusicGenerationStatusArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "job_id" {
			return "", errors.New("unsupported generation status argument")
		}
	}
	jobID, ok := args["job_id"].(string)
	if !ok || validateMusicJobID(jobID) != nil {
		return "", errors.New("job ID is invalid")
	}
	return jobID, nil
}

func normalizeMusicEditArgs(args map[string]any, allowRequestID bool) (string, MusicEditInput, error) {
	for key := range args {
		switch key {
		case "project_id", "source_artifact_id", "start_frame", "end_frame", "gain_db", "fade_frames":
		case "request_id":
			if !allowRequestID {
				return "", MusicEditInput{}, errors.New("request ID is unsupported without production control")
			}
		default:
			return "", MusicEditInput{}, errors.New("unsupported remix argument")
		}
	}
	projectID, projectOK := args["project_id"].(string)
	artifactID, artifactOK := args["source_artifact_id"].(string)
	start, startOK := integerToolArgument(args["start_frame"])
	end, endOK := integerToolArgument(args["end_frame"])
	fade, fadeOK := integerToolArgument(args["fade_frames"])
	gain, gainOK := musicPlanFloat(args["gain_db"])
	if !projectOK || validateMusicProjectID(projectID) != nil || !artifactOK || validateMusicArtifactID(artifactID) != nil ||
		!startOK || !endOK || !fadeOK || !gainOK || math.IsNaN(gain) || math.IsInf(gain, 0) {
		return "", MusicEditInput{}, errors.New("remix identity or numeric fields are invalid")
	}
	if start < 0 || end <= start || end > musicEditTimelineFrames || end-start > musicEditTimelineFrames ||
		fade < 0 || fade > musicEditMaxFadeFrames || int64(fade)*2 > int64(end-start) || gain < -12 || gain > 12 {
		return "", MusicEditInput{}, errors.New("remix fields exceed the bounded edit policy")
	}
	requestID := ""
	if raw, exists := args["request_id"]; exists {
		var ok bool
		requestID, ok = raw.(string)
		if !ok || validateMusicRequestID(requestID) != nil {
			return "", MusicEditInput{}, errors.New("request ID is invalid")
		}
	}
	return projectID, MusicEditInput{
		SourceArtifactID: artifactID, StartFrame: int64(start), EndFrame: int64(end),
		GainDB: gain, FadeFrames: int64(fade), RequestID: requestID,
	}, nil
}

func musicProofIdempotencyKey(prefix, requestID string) (string, bool) {
	if requestID == "" || len(requestID) > 200 || strings.TrimSpace(requestID) != requestID {
		return "", false
	}
	digest := sha256.Sum256([]byte(requestID))
	return prefix + hex.EncodeToString(digest[:]), true
}

func musicArtifactPlaybackPath(artifactID string) string {
	return "/viewer/music/artifacts/" + url.PathEscape(artifactID) + "/download?to=mio"
}
