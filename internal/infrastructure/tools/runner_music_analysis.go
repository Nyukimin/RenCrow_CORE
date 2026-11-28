package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerMusicAnalysisTools() {
	if r.config.MusicAnalysisBeats != nil || r.config.MusicAnalysisCaptions != nil {
		r.toolsV2["music.analysis.create"] = r.executeMusicAnalysisCreateV2
	}
	if r.config.MusicAnalysisReader != nil {
		r.toolsV2["music.analysis.get"] = r.executeMusicAnalysisGetV2
	}
}

func (r *ToolRunner) registerMusicSourceSearchTool() {
	r.toolsV2["music.source.search"] = r.executeMusicSourceSearchV2
}

func (r *ToolRunner) registerMusicAudioImportTools(importReady bool) {
	if importReady {
		r.toolsV2["music.audio.import"] = r.executeMusicAudioImportV2
	}
	r.toolsV2["music.audio.import.status"] = r.executeMusicAudioImportStatusV2
}

func musicAnalysisCreateMetadata(beatsReady, captionsReady bool) tool.ToolMetadata {
	operations := make([]any, 0, 2)
	if beatsReady {
		operations = append(operations, "beats")
	}
	if captionsReady {
		operations = append(operations, "captions")
	}
	return tool.ToolMetadata{
		ToolID: "music.analysis.create", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicで設定済みactorが所有する音声artifactのbeatsまたはcaptionsを解析する。1回につき1 operationを作成し、最大4分10秒応答を待つ。",
		Invariants: []string{
			"projectとartifactは設定済みactorがMusic APIで所有を確認する",
			"operationごとに実装・readyを観測したものだけ選べる",
			"artifact bytes、filesystem path、provider URLはTool引数に取らない",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id":         map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"source_artifact_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"operation":          map[string]any{"type": "string", "enum": operations},
			},
			"required": []any{"project_id", "source_artifact_id", "operation"},
		},
	}
}

func musicAnalysisGetMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.analysis.get", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicから設定済みactorが所有するbeatsまたはcaptions解析結果を取得する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"analysis_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
			"required":   []any{"analysis_id"},
		},
	}
}

func musicSourceSearchMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.source.search", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "Openverseの検索メタデータを取得する。結果は参照情報で、音声のダウンロードや利用許諾を行わない。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"q":         map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
				"page":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 1},
				"page_size": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 20},
			},
			"required": []any{"q"},
		},
	}
}

func musicAudioImportMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.audio.import", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "現在の認証済みViewer requestに添付されたFLACを、Musicの指定projectへ最大10 MiB・180秒で取り込む。添付IDのみを受け付け、pathやURLを読み込まない。rights/license clearanceは確認しない。",
		Invariants: []string{
			"attachment_idは現在の認証済みrequest/Taskに属するserver-origin attachmentだけを参照する",
			"source moduleとidempotency keyはCOREが固定し、Tool引数から指定できない",
			"native FLACのみを保存し、transcodeや利用権の主張はしない",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id":    map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"attachment_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "このrequestに添付したaudio fileのopaque ID"},
			},
			"required": []any{"project_id", "attachment_id"},
		},
	}
}

func musicAudioImportStatusMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.audio.import.status", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "以前のaudio import jobのactor-scoped statusを取得し、再実行せず確定状態を確認する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"job_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
			"required":   []any{"job_id"},
		},
	}
}

func (r *ToolRunner) executeMusicAnalysisCreateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	projectID, artifactID, operation, err := normalizeMusicAnalysisCreateArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.analysis.create arguments are invalid", nil), nil
	}
	gateway := r.config.MusicAnalysisBeats
	if operation == "captions" {
		gateway = r.config.MusicAnalysisCaptions
	}
	if gateway == nil {
		return tool.NewError(tool.ErrNotFound, "The requested Music analysis operation is unavailable", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, gateway.ActorID())
	if failure != nil {
		return failure, nil
	}
	if r.config.MusicProjects == nil || r.config.MusicArtifactMetadata == nil {
		return tool.NewError(tool.ErrNotFound, "Music project or artifact metadata is unavailable", nil), nil
	}
	project, err := r.config.MusicProjects.GetProject(ctx, projectID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	if project.ID != projectID {
		return tool.NewError(tool.ErrNotFound, "Music project was not found for this actor", nil), nil
	}
	artifact, err := r.config.MusicArtifactMetadata.GetMusicArtifact(ctx, artifactID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	if artifact.ProjectID != projectID || artifact.Stale || artifact.MediaType != "audio/flac" && artifact.MediaType != "audio/wav" && artifact.MediaType != "audio/x-wav" {
		return tool.NewError(tool.ErrValidationFailed, "Music source artifact is not usable in the requested project", nil), nil
	}
	key, ok := trustedToolActionIdempotencyKey(ctx, "rencrow-core-music-analysis-"+operation+"-", proof)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music analysis contract", nil), nil
	}
	analysis, err := gateway.CreateMusicAnalysis(ctx, projectID, MusicAnalysisRequest{SourceArtifactID: artifactID, Operation: operation}, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	if analysis.State != "succeeded" {
		return tool.NewSuccess(analysis), nil
	}
	var document MusicAnalysisDocument
	if err := json.Unmarshal(analysis.Result, &document); err != nil || document.SampleRate != artifact.SampleRate || document.DurationFrames != artifact.DurationFrames {
		return tool.NewError(tool.ErrValidationFailed, "Music analysis timeline does not match the owned source artifact metadata", nil), nil
	}
	return tool.NewSuccess(analysis), nil
}

func (r *ToolRunner) executeMusicAnalysisGetV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	analysisID, err := normalizeMusicAnalysisGetArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.analysis.get arguments are invalid", nil), nil
	}
	gateway := r.config.MusicAnalysisReader
	if gateway == nil {
		return tool.NewError(tool.ErrNotFound, "Music analysis reader is unavailable", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, gateway.ActorID()); failure != nil {
		return failure, nil
	}
	analysis, err := gateway.GetMusicAnalysis(ctx, analysisID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(analysis), nil
}

func (r *ToolRunner) executeMusicSourceSearchV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	query, page, pageSize, err := normalizeMusicSourceSearchArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.source.search arguments are invalid", nil), nil
	}
	if r.config.MusicSourceSearch == nil {
		return tool.NewError(tool.ErrNotFound, "Music source search is unavailable", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicSourceSearch.ActorID()); failure != nil {
		return failure, nil
	}
	result, err := r.config.MusicSourceSearch.SearchMusicSources(ctx, query, page, pageSize)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(result), nil
}

func (r *ToolRunner) executeMusicAudioImportV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	projectID, attachmentID, err := normalizeMusicAudioImportArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.audio.import arguments are invalid", nil), nil
	}
	if r.config.MusicAudioImport == nil || r.config.MusicProjects == nil || !r.config.MusicAudioImportReady {
		return tool.NewError(tool.ErrNotFound, "Music audio import is unavailable", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicAudioImport.ActorID())
	if failure != nil {
		return failure, nil
	}
	if _, err := r.config.MusicProjects.GetProject(ctx, projectID); err != nil {
		return musicGatewayToolError(err), nil
	}
	attachments, found := domainattachment.TrustedRequestAttachmentsByIDFromContext(ctx, attachmentID)
	if !found {
		return tool.NewError(tool.ErrPermissionDenied, "A current authenticated request attachment is required", nil), nil
	}
	if len(attachments) == 0 {
		return tool.NewError(tool.ErrNotFound, "The attachment is not part of the current authenticated request", nil), nil
	}
	if len(attachments) != 1 {
		return tool.NewError(tool.ErrValidationFailed, "attachment ID is ambiguous in the current request", nil), nil
	}
	selected := &attachments[0]
	if selected.Kind != domainattachment.KindAudio || selected.SizeBytes < 4 || selected.SizeBytes > maxMusicAudioImportBytes ||
		!musicArtifactSHA256Pattern.MatchString(selected.SHA256) {
		return tool.NewError(tool.ErrValidationFailed, "The selected attachment is not a bounded native FLAC file", nil), nil
	}
	audio, err := readTrustedMusicAttachment(r.config.WorkspaceDir, *selected)
	if err != nil || len(audio) < 4 || string(audio[:4]) != "fLaC" {
		return tool.NewError(tool.ErrValidationFailed, "The selected attachment is not a readable native FLAC file under the configured workspace", nil), nil
	}
	hash := sha256.Sum256(audio)
	if hex.EncodeToString(hash[:]) != selected.SHA256 {
		return tool.NewError(tool.ErrValidationFailed, "The selected attachment failed its trusted content checksum", nil), nil
	}
	key, ok := trustedToolActionIdempotencyKey(ctx, "rencrow-core-music-audio-import-", proof)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music import contract", nil), nil
	}
	status, err := r.config.MusicAudioImport.ImportMusicAudio(ctx, projectID, attachmentID, selected.SHA256, audio, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(musicAudioImportResult(status, r.config.MusicPlaybackLinkReady && r.config.MusicArtifactPlayback != nil)), nil
}

func readTrustedMusicAttachment(workspaceDir string, attachment domainattachment.Attachment) ([]byte, error) {
	if strings.TrimSpace(workspaceDir) == "" || attachment.Path == "" || filepath.Base(attachment.Path) != attachment.Filename ||
		domainattachment.SafeFilename(attachment.Filename) != attachment.Filename {
		return nil, errors.New("trusted attachment path metadata is invalid")
	}
	workspaceRoot, err := filepath.Abs(filepath.Clean(workspaceDir))
	if err != nil {
		return nil, errors.New("configured attachment workspace is invalid")
	}
	uploadRoot := filepath.Join(workspaceRoot, "viewer_uploads")
	uploadRootInfo, err := os.Lstat(uploadRoot)
	if err != nil || !uploadRootInfo.IsDir() || uploadRootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("configured attachment upload root is missing or unsafe")
	}
	path, err := filepath.Abs(filepath.Clean(attachment.Path))
	if err != nil {
		return nil, errors.New("trusted attachment path is invalid")
	}
	relative, err := filepath.Rel(uploadRoot, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("trusted attachment path is outside the configured upload root")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 4 || len(parts[0]) != 8 || !allASCIIDigits(parts[0]) || parts[1] != "viewer" || parts[2] != attachment.ID || parts[3] != attachment.Filename {
		return nil, errors.New("trusted attachment path does not match the upload-store layout")
	}
	current := uploadRoot
	var storedInfo os.FileInfo
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("trusted attachment path is missing or contains a symbolic link")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("trusted attachment parent is not a directory")
		}
		if index == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() != attachment.SizeBytes || info.Size() > maxMusicAudioImportBytes) {
			return nil, errors.New("trusted attachment file size or type is invalid")
		}
		if index == len(parts)-1 {
			storedInfo = info
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("trusted attachment file cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != attachment.SizeBytes || storedInfo == nil || !os.SameFile(storedInfo, opened) {
		return nil, errors.New("trusted attachment file changed before it was opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMusicAudioImportBytes+1))
	if err != nil || int64(len(data)) != attachment.SizeBytes || int64(len(data)) > maxMusicAudioImportBytes {
		return nil, errors.New("trusted attachment file could not be read within its bounded size")
	}
	return data, nil
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (r *ToolRunner) executeMusicAudioImportStatusV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	jobID, err := normalizeMusicAudioImportStatusArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.audio.import.status arguments are invalid", nil), nil
	}
	if r.config.MusicAudioImport == nil {
		return tool.NewError(tool.ErrNotFound, "Music audio import is unavailable", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicAudioImport.ActorID()); failure != nil {
		return failure, nil
	}
	status, err := r.config.MusicAudioImport.GetMusicAudioImportJob(ctx, jobID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(musicAudioImportResult(status, r.config.MusicPlaybackLinkReady && r.config.MusicArtifactPlayback != nil)), nil
}

func musicAudioImportResult(status MusicAudioImportJobStatus, playbackReady bool) MusicAudioImportResult {
	result := MusicAudioImportResult{MusicAudioImportJobStatus: status}
	if playbackReady && status.State == "succeeded" && status.ArtifactID != "" {
		result.PlaybackURL = musicArtifactPlaybackPath(status.ArtifactID)
	}
	return result
}

func normalizeMusicAnalysisCreateArgs(args map[string]any) (string, string, string, error) {
	for key := range args {
		if key != "project_id" && key != "source_artifact_id" && key != "operation" {
			return "", "", "", errors.New("unsupported argument")
		}
	}
	projectID, projectOK := args["project_id"].(string)
	artifactID, artifactOK := args["source_artifact_id"].(string)
	operation, operationOK := args["operation"].(string)
	if !projectOK || validateMusicProjectID(projectID) != nil || !artifactOK || validateMusicArtifactID(artifactID) != nil || !operationOK || operation != "beats" && operation != "captions" {
		return "", "", "", errors.New("project, artifact, or operation is invalid")
	}
	return projectID, artifactID, operation, nil
}

func normalizeMusicAnalysisGetArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "analysis_id" {
			return "", errors.New("unsupported argument")
		}
	}
	analysisID, ok := args["analysis_id"].(string)
	if !ok || validateMusicOpaqueID(analysisID, "analysis ID") != nil {
		return "", errors.New("analysis ID is invalid")
	}
	return analysisID, nil
}

func normalizeMusicSourceSearchArgs(args map[string]any) (string, int, int, error) {
	for key := range args {
		if key != "q" && key != "page" && key != "page_size" {
			return "", 0, 0, errors.New("unsupported argument")
		}
	}
	query, ok := args["q"].(string)
	if !ok || strings.TrimSpace(query) == "" || len([]byte(query)) > 200 {
		return "", 0, 0, errors.New("query is invalid")
	}
	page, pageOK := 1, true
	if raw, exists := args["page"]; exists {
		page, pageOK = integerToolArgument(raw)
	}
	pageSize, pageSizeOK := 20, true
	if raw, exists := args["page_size"]; exists {
		pageSize, pageSizeOK = integerToolArgument(raw)
	}
	if !pageOK || page < 1 || page > 100 || !pageSizeOK || pageSize < 1 || pageSize > 50 {
		return "", 0, 0, errors.New("page range is invalid")
	}
	return query, page, pageSize, nil
}

func normalizeMusicAudioImportArgs(args map[string]any) (string, string, error) {
	for key := range args {
		if key != "project_id" && key != "attachment_id" {
			return "", "", errors.New("unsupported argument")
		}
	}
	projectID, projectOK := args["project_id"].(string)
	attachmentID, attachmentOK := args["attachment_id"].(string)
	if !projectOK || validateMusicProjectID(projectID) != nil || !attachmentOK || validateMusicOpaqueID(attachmentID, "attachment ID") != nil {
		return "", "", errors.New("project or attachment ID is invalid")
	}
	return projectID, attachmentID, nil
}

func normalizeMusicAudioImportStatusArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "job_id" {
			return "", errors.New("unsupported argument")
		}
	}
	jobID, ok := args["job_id"].(string)
	if !ok || validateMusicJobID(jobID) != nil {
		return "", errors.New("job ID is invalid")
	}
	return jobID, nil
}
