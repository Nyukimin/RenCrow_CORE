package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

var musicLeadingModeDirectivePattern = regexp.MustCompile(`(?i)^\s*(AUTO|DIRECT)で`)
var musicBPMReferencePattern = regexp.MustCompile(`(?i)(?:\bBPM\s*(?:[:=]|は|を)?\s*[0-9]{2,3}\b|\b[0-9]{2,3}\s*BPM\b)`)
var musicBPMNumberPattern = regexp.MustCompile(`(?i)([0-9]{2,3})\s*BPM|BPM(?:\s*(?:は|を|[:=])\s*)?(?:about|approx(?:imately)?|around|roughly|~|約|およそ|だいたい|大体)?\s*([0-9]{2,3})`)
var musicApproxBPMPhrasePattern = regexp.MustCompile(`(?i)(?:about|approx(?:imately)?|around|roughly|~|約|およそ|だいたい|大体)\s*[0-9]{2,3}\s*BPM|[0-9]{2,3}\s*BPM\s*(?:ish|or so|±\s*[0-9]{1,3}|\+/-\s*[0-9]{1,3}|くらい|ぐらい|前後|程度|ほど)|[0-9]{2,3}\s*(?:-|–|—|~|〜|～|to|から)\s*[0-9]{2,3}\s*BPM|BPM\s*(?:は|を|[:=])\s*(?:about|approx(?:imately)?|around|roughly|約|およそ|だいたい|大体)\s*[0-9]{2,3}`)
var musicApproxBPMBeforePattern = regexp.MustCompile(`(?i)(?:about|approx(?:imately)?|around|roughly|~|〜|～|約|およそ|だいたい|大体)\s*$`)
var musicApproxBPMAfterPattern = regexp.MustCompile(`(?i)^\s*(?:ish|or so|about|approximately|around|くらい|ぐらい|前後|程度|ほど)`)
var musicBPMRangeBeforePattern = regexp.MustCompile(`(?i)[0-9]{2,3}\s*(?:-|–|—|~|〜|～|to|から)\s*$`)
var musicBPMRangeAfterPattern = regexp.MustCompile(`(?i)^\s*(?:-|–|—|~|〜|～|to|から)\s*[0-9]{2,3}(?:\s*BPM)?\b`)

func (r *ToolRunner) registerMusicControlTools() {
	r.toolsV2["music.control.get"] = r.executeMusicControlGetV2
	r.toolsV2["music.control.update"] = r.executeMusicControlUpdateV2
	r.toolsV2["music.request.create"] = r.executeMusicRequestCreateV2
	r.toolsV2["music.request.get"] = r.executeMusicRequestGetV2
}

func musicControlGetMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.control.get", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicから同じ設定actorが所有するprojectのmode、保護lock、hard constraint、採用済みversionを取得する。",
		Invariants:  []string{"project_idは利用者が示した不透明IDを使い、他projectの状態を参照しない"},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"project_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
			"required":   []any{"project_id"},
		},
	}
}

func musicControlUpdateMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.control.update", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "Music project controlをexpected_revisionのCASで更新する。更新前にmusic.control.getで現在revisionを取得し、明示された変更だけを送る。DIRECT/AUTOはプロジェクト設定の変更で、個別依頼のmodeはmusic.request.createで指定する。",
		Invariants: []string{
			"actor、idempotency key、expected actor headerは設定と認証済みHTTP proofからCOREが固定する",
			"revision conflict時は状態を再読込してから判断し、古い更新を自動再送しない",
			"select_version_idは同一project内の確認済みartifact version IDだけを指定する",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id":                   map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"expected_revision":            map[string]any{"type": "integer", "minimum": 0},
				"mode":                         map[string]any{"type": "string", "enum": []any{"DIRECT", "AUTO"}},
				"add_locks":                    musicControlLockArraySchema(),
				"remove_lock_ids":              map[string]any{"type": "array", "maxItems": 128, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
				"set_hard_constraints":         musicControlHardConstraintArraySchema(),
				"remove_constraint_properties": map[string]any{"type": "array", "maxItems": 128, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
				"select_version_id":            map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
			"required": []any{"project_id", "expected_revision"},
		},
	}
}

func musicRequestCreateMetadata() tool.ToolMetadata {
	modeSchema := map[string]any{"type": "string", "enum": []any{"DIRECT", "AUTO"}}
	return tool.ToolMetadata{
		ToolID: "music.request.create", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "COREが信頼済みWorker contextから受け取った元の利用者発話と、Workerが構造化した音楽依頼をMusicへ保存する。生成・編集前に必ず呼び、返されたrequest_idを次のToolへ渡す。",
		Invariants: []string{
			"raw_textは信頼済みWorker contextの原文だけを使い、contextがない場合は失敗する。model echoを原文として保存しない",
			"request IDとidempotency keyはCOREが導出し、actor/projectを利用者の許可scopeに固定する",
			"generationまたはgain_remixの正規化要求だけを記録し、ここでは生成・編集しない",
			"AUTOでも絶対条件と保護要件はhard_constraintsまたはpreserve_locksに保持する。概算BPMはsoft_preferencesへ置き、exact BPMはMusicが実音源のtempoを検証できないためrequest作成前に確認する",
			"未指定内容はgain_remixのedit_scope内だけでauto_scopeにできる。scope外を推測で変更しない",
			"protected lockまたはunsupported editを回避するため、全曲generationへのfallbackを提案しない",
			"L1=creative: 雰囲気・感情・ジャンル・世界観・用途・歌声の印象。L2=arrangement: テンポ・キー・コード・楽器・構成・歌唱方針。L3=engineering: 小節・秒・トラック・音符・発音・音程曲線・音量・EQ・コンプ",
			"layersは1〜3個の一意なL1/L2/L3だけ。masterなどの自由語は対応layerへ推測変換せず、未知または重複指定は確認する",
			"hard_constraintsはoutput_formatとwrite_scopeだけ。Musicは実音源のtempoを検証できないため、明示exact BPMはrequestを保存せず、soft preferenceへ変換しない",
			"概算BPMはsoft_preferencesに保持し、exact BPMへ昇格しない",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id":       map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"explicit_mode":    modeSchema,
				"inferred_mode":    modeSchema,
				"layers":           map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []any{"L1", "L2", "L3"}}},
				"operation":        map[string]any{"type": "string", "enum": []any{"generation", "gain_remix"}},
				"base_version_id":  map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"edit_scope":       musicControlFrameRangeSchema(),
				"hard_constraints": musicControlHardConstraintArraySchema(),
				"soft_preferences": musicControlConstraintArraySchema(),
				"preserve_locks":   musicControlLockArraySchema(),
				"auto_scope":       map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
			},
			"required": []any{"project_id", "layers", "operation"},
		},
	}
}

func musicRequestGetMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.request.get", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "同じ設定actorが同一projectで作成したMusic依頼の正規化内容と適用時snapshotを取得する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"request_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
			},
			"required": []any{"project_id", "request_id"},
		},
	}
}

func musicControlConstraintArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 128,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"property": map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
				"value":    map[string]any{"type": "string", "minLength": 1, "maxLength": 2000},
			},
			"required": []any{"property", "value"},
		},
	}
}

func musicControlHardConstraintArraySchema() map[string]any {
	schema := musicControlConstraintArraySchema()
	items := schema["items"].(map[string]any)
	properties := items["properties"].(map[string]any)
	properties["property"] = map[string]any{"type": "string", "enum": []any{"output_format", "write_scope"}}
	return schema
}

func musicControlLockArraySchema() map[string]any {
	return map[string]any{
		"type": "array", "maxItems": 128,
		"items": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"id":                   map[string]any{"type": "string", "maxLength": 256},
				"type":                 map[string]any{"type": "string", "enum": []any{"audio_exact", "structure", "perceptual"}},
				"stage":                map[string]any{"type": "string", "enum": []any{"final_mix", "track_audio"}},
				"reference_version_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"scope":                musicControlFrameRangeSchema(),
				"properties":           map[string]any{"type": "array", "maxItems": 64, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
			},
			"required": []any{"type", "reference_version_id"},
		},
	}
}

func musicControlFrameRangeSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"start_frame": map[string]any{"type": "integer", "minimum": 0, "maximum": 60*60*48000 - 1},
			"end_frame":   map[string]any{"type": "integer", "minimum": 1, "maximum": 60 * 60 * 48000},
		},
		"required": []any{"start_frame", "end_frame"},
	}
}

func (r *ToolRunner) executeMusicControlGetV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicControl == nil {
		return tool.NewError(tool.ErrNotFound, "Music production control is unavailable", nil), nil
	}
	projectID, err := normalizeMusicProjectGetArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.control.get arguments are invalid", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicControl.ActorID()); failure != nil {
		return failure, nil
	}
	state, err := r.config.MusicControl.GetMusicControl(ctx, projectID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(state), nil
}

func (r *ToolRunner) executeMusicControlUpdateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicControl == nil {
		return tool.NewError(tool.ErrNotFound, "Music production control is unavailable", nil), nil
	}
	projectID, input, err := normalizeMusicControlUpdateArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.control.update arguments are invalid", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicControl.ActorID())
	if failure != nil {
		return failure, nil
	}
	if len(input.RemoveLockIDs) > 0 || len(input.RemoveConstraintProperties) > 0 {
		rawText, found := tool.OriginalUserMessageFromContext(ctx)
		if !found || !musicControlRemovalExplicitlyRequested(rawText, input) {
			return tool.NewError(tool.ErrPermissionDenied, "Music lock/constraint removal requires explicit user text naming each target", nil), nil
		}
	}
	key, ok := musicProofIdempotencyKey("rencrow-core-music-control-", proof.RequestID)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music control contract", nil), nil
	}
	state, err := r.config.MusicControl.UpdateMusicControl(ctx, projectID, input, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(state), nil
}

func musicControlRemovalExplicitlyRequested(rawText string, input MusicControlUpdate) bool {
	text := strings.ToLower(rawText)
	explicitRemoval := false
	for _, phrase := range []string{"解除", "削除", "外して", "外す", "unlock", "remove", "delete"} {
		if strings.Contains(text, phrase) {
			explicitRemoval = true
			break
		}
	}
	if !explicitRemoval {
		return false
	}
	for _, lockID := range input.RemoveLockIDs {
		if lockID == "" || !strings.Contains(rawText, lockID) {
			return false
		}
	}
	for _, property := range input.RemoveConstraintProperties {
		if property == "" || !strings.Contains(rawText, property) {
			return false
		}
	}
	return true
}

func (r *ToolRunner) executeMusicRequestCreateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicControl == nil {
		return tool.NewError(tool.ErrNotFound, "Music production control is unavailable", nil), nil
	}
	projectID, input, err := normalizeMusicControlRequestArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.request.create arguments are invalid", nil), nil
	}
	rawText, found := tool.OriginalUserMessageFromContext(ctx)
	if !found {
		return tool.NewError(tool.ErrToolExecutionScopeMissing, "Music request requires trusted original user text", nil), nil
	}
	input.RawText = rawText
	input.SoftPreferences = preserveMusicApproximateBPMPreference(input.SoftPreferences, rawText)
	if mode, explicit := explicitLeadingMusicMode(rawText); explicit {
		input.ExplicitMode = &mode
		input.InferredMode = nil
	}
	if hasUnqualifiedExactMusicBPM(rawText) {
		return tool.NewError(tool.ErrValidationFailed, "Music cannot verify actual audio tempo, so this exact BPM request was not saved. Ask whether an approximate tempo preference is acceptable.", nil), nil
	}
	if err := validateMusicControlRequestInput(input); err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.request.create source text or normalized fields are invalid", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicControl.ActorID())
	if failure != nil {
		return failure, nil
	}
	key, ok := musicProofIdempotencyKey("rencrow-core-music-request-", proof.RequestID)
	if !ok {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music request contract", nil), nil
	}
	request, err := r.config.MusicControl.CreateMusicControlRequest(ctx, projectID, input, key)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(request), nil
}

func preserveMusicApproximateBPMPreference(preferences []MusicConstraint, rawText string) []MusicConstraint {
	for _, match := range musicApproxBPMPhrasePattern.FindAllString(rawText, -1) {
		phrase := strings.TrimSpace(match)
		bpmNumber := musicBPMNumber(phrase)
		duplicate := false
		for _, preference := range preferences {
			if !strings.EqualFold(preference.Property, "tempo") {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(preference.Value), phrase) ||
				(bpmNumber != "" && musicBPMNumber(preference.Value) == bpmNumber) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			preferences = append(preferences, MusicConstraint{Property: "tempo", Value: phrase})
		}
	}
	return preferences
}

func musicBPMNumber(text string) string {
	match := musicBPMNumberPattern.FindStringSubmatch(text)
	if len(match) != 3 {
		return ""
	}
	if match[1] != "" {
		return match[1]
	}
	return match[2]
}

func explicitLeadingMusicMode(rawText string) (string, bool) {
	match := musicLeadingModeDirectivePattern.FindStringSubmatchIndex(rawText)
	if len(match) < 4 {
		return "", false
	}
	mode := strings.ToUpper(rawText[match[2]:match[3]])
	remainder := strings.TrimSpace(rawText[match[1]:])
	for _, nonDirective := range []string{"はなく", "なく", "ない", "いいですか", "よいですか", "可能ですか", "できますか", "どうですか"} {
		if strings.HasPrefix(remainder, nonDirective) {
			return "", false
		}
	}
	return mode, true
}

func hasUnqualifiedExactMusicBPM(rawText string) bool {
	for _, match := range musicBPMReferencePattern.FindAllStringIndex(rawText, -1) {
		prefixRunes := []rune(rawText[:match[0]])
		if len(prefixRunes) > 48 {
			prefixRunes = prefixRunes[len(prefixRunes)-48:]
		}
		suffixRunes := []rune(rawText[match[1]:])
		if len(suffixRunes) > 32 {
			suffixRunes = suffixRunes[:32]
		}
		prefix, suffix := string(prefixRunes), string(suffixRunes)
		if musicApproxBPMBeforePattern.MatchString(prefix) || musicApproxBPMAfterPattern.MatchString(suffix) ||
			musicBPMRangeBeforePattern.MatchString(prefix) || musicBPMRangeAfterPattern.MatchString(suffix) {
			continue
		}
		return true
	}
	return false
}

func (r *ToolRunner) executeMusicRequestGetV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicControl == nil {
		return tool.NewError(tool.ErrNotFound, "Music production control is unavailable", nil), nil
	}
	projectID, requestID, err := normalizeMusicRequestGetArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.request.get arguments are invalid", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicControl.ActorID()); failure != nil {
		return failure, nil
	}
	request, err := r.config.MusicControl.GetMusicControlRequest(ctx, projectID, requestID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(request), nil
}

func normalizeMusicControlUpdateArgs(args map[string]any) (string, MusicControlUpdate, error) {
	var wrapper struct {
		ProjectID string `json:"project_id"`
		MusicControlUpdate
	}
	if err := decodeMusicToolArgs(args, &wrapper); err != nil {
		return "", MusicControlUpdate{}, err
	}
	if validateMusicProjectID(wrapper.ProjectID) != nil || validateMusicControlUpdate(wrapper.MusicControlUpdate) != nil {
		return "", MusicControlUpdate{}, errors.New("Music control update is invalid")
	}
	return wrapper.ProjectID, wrapper.MusicControlUpdate, nil
}

func normalizeMusicControlRequestArgs(args map[string]any) (string, MusicControlRequestInput, error) {
	for key := range args {
		switch key {
		case "project_id", "explicit_mode", "inferred_mode", "layers", "operation", "base_version_id", "edit_scope", "hard_constraints", "soft_preferences", "preserve_locks", "auto_scope":
		default:
			return "", MusicControlRequestInput{}, errors.New("unsupported request create argument")
		}
	}
	var wrapper struct {
		ProjectID string `json:"project_id"`
		MusicControlRequestInput
	}
	if err := decodeMusicToolArgs(args, &wrapper); err != nil {
		return "", MusicControlRequestInput{}, err
	}
	if wrapper.Layers == nil {
		wrapper.Layers = []string{}
	}
	if validateMusicProjectID(wrapper.ProjectID) != nil || (wrapper.Operation != "generation" && wrapper.Operation != "gain_remix") || validateMusicRequestLayers(wrapper.Layers) != nil {
		return "", MusicControlRequestInput{}, errors.New("Music control request is invalid")
	}
	wrapper.RawText = ""
	return wrapper.ProjectID, wrapper.MusicControlRequestInput, nil
}

func normalizeMusicRequestGetArgs(args map[string]any) (string, string, error) {
	for key := range args {
		if key != "project_id" && key != "request_id" {
			return "", "", errors.New("unsupported request lookup argument")
		}
	}
	projectID, projectOK := args["project_id"].(string)
	requestID, requestOK := args["request_id"].(string)
	if !projectOK || validateMusicProjectID(projectID) != nil || !requestOK || validateMusicRequestID(requestID) != nil {
		return "", "", errors.New("project or request ID is invalid")
	}
	return projectID, requestID, nil
}

func decodeMusicToolArgs(args map[string]any, target any) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing Music tool arguments")
	}
	return nil
}
