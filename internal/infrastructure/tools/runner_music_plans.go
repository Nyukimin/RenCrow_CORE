package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const maxMusicPlanToolRequestBytes = 96 << 10

func (r *ToolRunner) registerMusicPlanTools() {
	r.toolsV2["music.plan.create"] = r.executeMusicPlanCreateV2
	r.toolsV2["music.plan.get"] = r.executeMusicPlanGetV2
}

func musicPlanCreateMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.plan.create", Version: "1.0.0", Category: "mutation", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicへschema 1.0のproduction planを保存する。音源生成は行わず、指定projectに紐づく不変planを1件作成する。",
		Invariants: []string{
			"project、actor、request ID、kind、schema version、rights、budget、idempotency keyはTool引数で上書きできない",
			"明示されたbrief、lyrics、constraints、protected features、assumptionsを保持する",
			"認証済みrequest IDごとに同じ作成keyを使い、本文を変更した再利用はConflictになる",
		},
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"project_id":         map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
				"brief_text":         map[string]any{"type": "string", "minLength": 1, "maxLength": 10000},
				"language":           map[string]any{"type": "string", "maxLength": 35, "pattern": `^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`},
				"song_kind":          map[string]any{"type": "string", "enum": []any{"vocal", "instrumental"}},
				"mood":               map[string]any{"type": "string", "maxLength": 500},
				"genre":              map[string]any{"type": "string", "maxLength": 500},
				"era":                map[string]any{"type": "string", "maxLength": 500},
				"use_case":           map[string]any{"type": "string", "maxLength": 500},
				"lyrics":             map[string]any{"type": "array", "maxItems": 100, "items": musicPlanLyricsSchema()},
				"lyrics_lock":        map[string]any{"type": "boolean"},
				"structure":          musicPlanStringArraySchema(100, 200),
				"duration_target_s":  map[string]any{"type": "integer", "minimum": 1, "maximum": 3600},
				"tempo":              map[string]any{"type": "number", "minimum": 20, "maximum": 300},
				"key":                map[string]any{"type": "string", "maxLength": 500},
				"vocal":              map[string]any{"type": "string", "maxLength": 500},
				"instruments":        musicPlanStringArraySchema(100, 200),
				"candidate_count":    map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
				"variation_axes":     map[string]any{"type": "array", "maxItems": 20, "uniqueItems": true, "items": map[string]any{"type": "string", "enum": []any{"mood", "genre", "era", "tempo", "key", "vocal", "instruments", "structure"}}},
				"constraints":        musicPlanStringArraySchema(100, 2000),
				"protected_features": musicPlanStringArraySchema(100, 2000),
				"assumptions":        musicPlanStringArraySchema(100, 2000),
			},
			"required": []any{"project_id", "brief_text", "song_kind"},
		},
	}
}

func musicPlanGetMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music.plan.get", Version: "1.0.0", Category: "query", DryRun: true, Origin: tool.OriginCoreRuntime,
		Description: "RenCrow_Musicから現在の認証済み利用者が所有するopaque production plan IDを取得する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"plan_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}},
			"required":   []any{"plan_id"},
		},
	}
}

func musicPlanLyricsSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"section": map[string]any{"type": "string", "minLength": 1, "maxLength": 100},
			"text":    map[string]any{"type": "string", "minLength": 1, "maxLength": 5000},
		},
		"required": []any{"section", "text"},
	}
}

func musicPlanStringArraySchema(maxItems, maxLength int) map[string]any {
	return map[string]any{
		"type": "array", "maxItems": maxItems,
		"items": map[string]any{"type": "string", "minLength": 1, "maxLength": maxLength},
	}
}

func (r *ToolRunner) executeMusicPlanCreateV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicPlans == nil {
		return tool.NewError(tool.ErrNotFound, "Music production-plan storage is unavailable", nil), nil
	}
	projectID, input, err := normalizeMusicPlanCreateArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.plan.create arguments are invalid", nil), nil
	}
	proof, failure := authorizeMusicProjectRequest(ctx, r.config.MusicPlans.ActorID())
	if failure != nil {
		return failure, nil
	}
	if proof.RequestID == "" || len(proof.RequestID) > 200 {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "authenticated request ID is outside the Music plan contract", nil), nil
	}
	input.Kind = "production"
	input.SchemaVersion = "1.0"
	input.SourceRequestID = proof.RequestID
	body, err := json.Marshal(input)
	if err != nil || len(body) > maxMusicPlanToolRequestBytes {
		return tool.NewError(tool.ErrValidationFailed, "music production plan exceeds the supported request size", nil), nil
	}
	requestHash := sha256.Sum256([]byte(proof.RequestID))
	idempotencyKey := "rencrow-core-plan-" + hex.EncodeToString(requestHash[:])
	plan, err := r.config.MusicPlans.CreateProductionPlan(ctx, projectID, input, idempotencyKey)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(plan), nil
}

func (r *ToolRunner) executeMusicPlanGetV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.MusicPlans == nil {
		return tool.NewError(tool.ErrNotFound, "Music production-plan storage is unavailable", nil), nil
	}
	planID, err := normalizeMusicPlanGetArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "music.plan.get arguments are invalid", nil), nil
	}
	if _, failure := authorizeMusicProjectRequest(ctx, r.config.MusicPlans.ActorID()); failure != nil {
		return failure, nil
	}
	plan, err := r.config.MusicPlans.GetProductionPlan(ctx, planID)
	if err != nil {
		return musicGatewayToolError(err), nil
	}
	return tool.NewSuccess(plan), nil
}

func normalizeMusicPlanCreateArgs(args map[string]any) (string, MusicProductionPlanInput, error) {
	for key := range args {
		switch key {
		case "project_id", "brief_text", "language", "song_kind", "mood", "genre", "era", "use_case", "lyrics", "lyrics_lock", "structure", "duration_target_s", "tempo", "key", "vocal", "instruments", "candidate_count", "variation_axes", "constraints", "protected_features", "assumptions":
		default:
			return "", MusicProductionPlanInput{}, errors.New("unsupported plan argument")
		}
	}
	projectID, ok := args["project_id"].(string)
	if !ok || validateMusicProjectID(projectID) != nil {
		return "", MusicProductionPlanInput{}, errors.New("project_id is invalid")
	}
	brief, ok := args["brief_text"].(string)
	if !ok || strings.TrimSpace(brief) == "" || len([]rune(brief)) > 10000 {
		return "", MusicProductionPlanInput{}, errors.New("brief_text is invalid")
	}
	songKind, ok := args["song_kind"].(string)
	if !ok || (songKind != "vocal" && songKind != "instrumental") {
		return "", MusicProductionPlanInput{}, errors.New("song_kind is invalid")
	}
	input := MusicProductionPlanInput{BriefText: brief, SongKind: songKind}
	if err := setMusicPlanString(args, "language", &input.Language, 35); err != nil {
		return "", MusicProductionPlanInput{}, err
	}
	if input.Language != "" && !musicLanguageTagPattern.MatchString(input.Language) {
		return "", MusicProductionPlanInput{}, errors.New("language is invalid")
	}
	for key, destination := range map[string]*string{
		"mood": &input.Mood, "genre": &input.Genre, "era": &input.Era, "use_case": &input.UseCase,
		"key": &input.Key, "vocal": &input.Vocal,
	} {
		if err := setMusicPlanString(args, key, destination, 500); err != nil {
			return "", MusicProductionPlanInput{}, err
		}
	}
	if raw, exists := args["lyrics"]; exists {
		items, ok := musicPlanArray(raw)
		if !ok || len(items) > 100 {
			return "", MusicProductionPlanInput{}, errors.New("lyrics must be a bounded array")
		}
		input.Lyrics = make([]MusicPlanLyricsSection, 0, len(items))
		for _, item := range items {
			lyric, ok := item.(map[string]any)
			if !ok {
				return "", MusicProductionPlanInput{}, errors.New("lyrics entries must be objects")
			}
			for key := range lyric {
				if key != "section" && key != "text" {
					return "", MusicProductionPlanInput{}, errors.New("lyrics entry contains unsupported fields")
				}
			}
			section, sectionOK := lyric["section"].(string)
			text, textOK := lyric["text"].(string)
			if !sectionOK || strings.TrimSpace(section) == "" || len([]rune(section)) > 100 || !textOK || strings.TrimSpace(text) == "" || len([]rune(text)) > 5000 {
				return "", MusicProductionPlanInput{}, errors.New("lyrics section or text is invalid")
			}
			input.Lyrics = append(input.Lyrics, MusicPlanLyricsSection{Section: section, Text: text})
		}
	}
	if raw, exists := args["lyrics_lock"]; exists {
		locked, ok := raw.(bool)
		if !ok {
			return "", MusicProductionPlanInput{}, errors.New("lyrics_lock must be a boolean")
		}
		input.LyricsLock = &locked
	}
	for key, destination := range map[string]*[]string{
		"structure": &input.Structure, "instruments": &input.Instruments,
		"constraints": &input.Constraints, "protected_features": &input.ProtectedFeatures, "assumptions": &input.Assumptions,
	} {
		maxLength := 200
		if key == "constraints" || key == "protected_features" || key == "assumptions" {
			maxLength = 2000
		}
		if err := setMusicPlanStringArray(args, key, destination, 100, maxLength); err != nil {
			return "", MusicProductionPlanInput{}, err
		}
	}
	if _, exists := args["variation_axes"]; exists {
		if err := setMusicPlanStringArray(args, "variation_axes", &input.VariationAxes, 20, 32); err != nil {
			return "", MusicProductionPlanInput{}, err
		}
		allowed := map[string]struct{}{"mood": {}, "genre": {}, "era": {}, "tempo": {}, "key": {}, "vocal": {}, "instruments": {}, "structure": {}}
		for _, axis := range input.VariationAxes {
			if _, ok := allowed[axis]; !ok {
				return "", MusicProductionPlanInput{}, errors.New("variation_axes contains an unsupported value")
			}
		}
	}
	if raw, exists := args["duration_target_s"]; exists {
		value, ok := integerToolArgument(raw)
		if !ok || value < 1 || value > 3600 {
			return "", MusicProductionPlanInput{}, errors.New("duration_target_s is outside the supported range")
		}
		input.DurationTargetS = value
	}
	if raw, exists := args["candidate_count"]; exists {
		value, ok := integerToolArgument(raw)
		if !ok || value < 1 || value > 5 {
			return "", MusicProductionPlanInput{}, errors.New("candidate_count is outside the supported range")
		}
		input.CandidateCount = value
	}
	if raw, exists := args["tempo"]; exists {
		value, ok := musicPlanFloat(raw)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 20 || value > 300 {
			return "", MusicProductionPlanInput{}, errors.New("tempo is outside the supported range")
		}
		input.Tempo = &value
	}
	if songKind == "instrumental" && (len(input.Lyrics) > 0 || input.LyricsLock != nil && *input.LyricsLock) {
		return "", MusicProductionPlanInput{}, errors.New("instrumental plans cannot include or lock lyrics")
	}
	if len(input.VariationAxes) > 0 {
		for _, axis := range input.VariationAxes {
			if axis == "lyrics" {
				return "", MusicProductionPlanInput{}, errors.New("lyrics variation requires a separate plan")
			}
		}
	}
	return projectID, input, nil
}

func normalizeMusicPlanGetArgs(args map[string]any) (string, error) {
	for key := range args {
		if key != "plan_id" {
			return "", errors.New("unsupported plan get argument")
		}
	}
	planID, ok := args["plan_id"].(string)
	if !ok || validateMusicPlanID(planID) != nil {
		return "", errors.New("plan_id is invalid")
	}
	return planID, nil
}

func setMusicPlanString(args map[string]any, key string, destination *string, maxLength int) error {
	raw, exists := args[key]
	if !exists {
		return nil
	}
	value, ok := raw.(string)
	if !ok || len([]rune(value)) > maxLength {
		return errors.New("string plan field is invalid")
	}
	*destination = value
	return nil
}

func setMusicPlanStringArray(args map[string]any, key string, destination *[]string, maxItems, maxLength int) error {
	raw, exists := args[key]
	if !exists {
		return nil
	}
	items, ok := musicPlanArray(raw)
	if !ok || len(items) > maxItems {
		return errors.New("plan array is outside the supported bounds")
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" || len([]rune(value)) > maxLength {
			return errors.New("plan array contains an invalid string")
		}
		values = append(values, value)
	}
	*destination = values
	return nil
}

func musicPlanArray(value any) ([]any, bool) {
	switch items := value.(type) {
	case []any:
		return items, true
	case []map[string]any:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out, true
	case []string:
		out := make([]any, len(items))
		for i := range items {
			out[i] = items[i]
		}
		return out, true
	default:
		return nil, false
	}
}

func musicPlanFloat(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

var musicLanguageTagPattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
