package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerMusicProjectTools() {
	r.toolsV2["music.project.create"] = r.executeMusicProjectCreateV2
	r.toolsV2["music.project.get"] = r.executeMusicProjectGetV2
}

func musicProjectCreateMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.project.create", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicに作曲プロジェクトを1件作成する。作成は現在の認証済み利用者の設定済みMusic actorに限定される。",
		Invariants:  []string{"actor_idとidempotency_keyは認証済みCORE requestから内部導出され、Tool引数で指定できない", "1 requestにつき1つのcreate keyを使い、本文を変えた再利用はConflictになる", "intended_useは専用enumで指定し、production planのconstraintsに重複して記載しない"},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"title":        map[string]any{"type": "string", "maxLength": 200},
				"brief":        map[string]any{"type": "string", "minLength": 1, "maxLength": 10000},
				"intended_use": map[string]any{"type": "string", "enum": []any{"personal_noncommercial"}},
				"mode":         map[string]any{"type": "string", "enum": []any{"interactive"}},
			},
			"required": []any{"brief"},
		},
	}
}

func musicProjectGetMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.project.get", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicから現在の認証済み利用者が所有する不透明なproject IDのスナップショットを取得する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
			"required": []any{"project_id"},
		},
	}
}

func (r *ToolRunner) executeMusicProjectCreateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicProjects == nil {
		return tool.NewError(tool.ErrNotFound, "Music project storage is unavailable", nil), nil
	}
	input, err := normalizeMusicProjectCreateArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.project.create arguments are invalid", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicProjects.ActorID())
	if failure != nil {
		return failure, nil
	}
	requestHash := sha256.Sum256([]byte(proof.RequestID))
	idempotencyKey := "rencrow-core-" + hex.EncodeToString(requestHash[:])
	project, err := r.config.MusicProjects.CreateProject(ctx, input, idempotencyKey)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(project), nil
}

func (r *ToolRunner) executeMusicProjectGetV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicProjects == nil {
		return tool.NewError(tool.ErrNotFound, "Music project storage is unavailable", nil), nil
	}
	projectID, err := normalizeMusicProjectGetArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.project.get arguments are invalid", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicProjects.ActorID()); failure != nil {
		return failure, nil
	}
	project, err := r.config.MusicProjects.GetProject(ctx, projectID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(project), nil
}

func normalizeMusicProjectCreateArgs(args map[string]any) (MusicProjectCreateInput, error) {
	for key := range args {
		switch key {
		case "title", "brief", "intended_use", "mode":
		default:
			return MusicProjectCreateInput{}, errors.New("unsupported argument")
		}
	}
	brief, ok := args["brief"].(string)
	if !ok {
		return MusicProjectCreateInput{}, errors.New("brief is required")
	}
	brief = strings.TrimSpace(brief)
	if brief == "" || len([]rune(brief)) > 10000 {
		return MusicProjectCreateInput{}, errors.New("brief is outside the supported size")
	}
	input := MusicProjectCreateInput{Brief: brief}
	if raw, exists := args["title"]; exists {
		title, ok := raw.(string)
		if !ok || len([]rune(strings.TrimSpace(title))) > 200 {
			return MusicProjectCreateInput{}, errors.New("title is invalid")
		}
		input.Title = strings.TrimSpace(title)
	}
	if raw, exists := args["intended_use"]; exists {
		intendedUse, ok := raw.(string)
		if !ok || intendedUse != "personal_noncommercial" {
			return MusicProjectCreateInput{}, errors.New("intended_use is unsupported")
		}
		input.IntendedUse = intendedUse
	}
	if raw, exists := args["mode"]; exists {
		mode, ok := raw.(string)
		if !ok || mode != "interactive" {
			return MusicProjectCreateInput{}, errors.New("mode is unsupported")
		}
		input.Mode = mode
	}
	return input, nil
}

func normalizeMusicProjectGetArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "project_id" {
			return "", errors.New("unsupported argument")
		}
	}
	projectID, ok := args["project_id"].(string)
	if !ok || validateMusicProjectID(projectID) != nil {
		return "", errors.New("project_id is invalid")
	}
	return projectID, nil
}

func authorizeMusicProjectRequest(ctx context.Context, configuredActorID string) (tool.ToolExecutionScope, *tool.ToolResponse) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return tool.ToolExecutionScope{}, tool.NewError(tool.ErrToolExecutionScopeMissing, "trusted Tool execution scope is required for Music projects", nil)
	}
	if err := scope.Validate(); err != nil {
		return tool.ToolExecutionScope{}, tool.NewError(tool.ErrToolExecutionScopeInvalid, "trusted Tool execution scope is invalid", nil)
	}
	if !scope.Allows(tool.DataScopeUser) || scope.AuthenticatedUserID == "" {
		return tool.ToolExecutionScope{}, tool.NewError(tool.ErrPermissionDenied, "Music project access requires delegated authenticated user scope", nil)
	}
	proof, found := tool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !found || configuredActorID == "" || scope.AuthenticatedUserID != configuredActorID || proof.AuthenticatedUserID != configuredActorID {
		return tool.ToolExecutionScope{}, tool.NewError(tool.ErrPermissionDenied, "Music project access is not authorized for this user", nil)
	}
	return proof, nil
}

func musicGatewayToolError(err error) *tool.ToolResponse {
	if errors.Is(err, context.DeadlineExceeded) {
		return tool.NewError(tool.ErrTimeout, "Music gateway request timed out", nil)
	}
	var statusErr *MusicGatewayStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case 400, 413, 415:
			return tool.NewError(tool.ErrValidationFailed, "Music gateway rejected the request", nil)
		case 401, 403:
			return tool.NewError(tool.ErrPermissionDenied, "Music gateway rejected its configured credential", nil)
		case 404:
			return tool.NewError(tool.ErrNotFound, "Music resource was not found for this actor", nil)
		case 409:
			switch statusErr.Code {
			case "MUSIC_NEEDS_CONFIRMATION":
				return tool.NewError(tool.ErrConflict, "Musicの適用にはbase versionまたは編集範囲の確認が必要です", nil)
			case "MUSIC_PRESERVATION_CONFLICT":
				return tool.NewError(tool.ErrConflict, "Musicの保護lockまたはhard constraintと競合しています", nil)
			case "MUSIC_CONTROL_CONFLICT":
				return tool.NewError(tool.ErrConflict, "Music control revisionが更新されています。music.control.getで最新状態を取得してください", nil)
			case "MUSIC_CONTROL_STALE":
				return tool.NewError(tool.ErrConflict, "Music requestの適用対象revisionが古くなっています。最新状態を確認してからrequestを作り直してください", nil)
			}
			return tool.NewError(tool.ErrConflict, "Music request conflicts with an earlier request", nil)
		case 422:
			if statusErr.Code == "MUSIC_UNSUPPORTED_OPERATION" {
				return tool.NewError(tool.ErrValidationFailed, "Musicは指定された操作または保護範囲に対応していません", nil)
			}
			return tool.NewError(tool.ErrValidationFailed, "Music gateway rejected the request", nil)
		}
	}
	return tool.NewError(tool.ErrInternalError, "Music gateway is unavailable", nil)
}
