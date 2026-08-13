package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerMusicCatalogLookupTool() {
	r.toolsV2["music_catalog.lookup"] = r.executeMusicCatalogLookupV2
}

func musicCatalogLookupMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "music_catalog.lookup", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "RenCrow音楽カタログを歌手名または楽曲名の完全一致索引で照会し、歌唱関係と公開metadataを返す。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []any{"artist", "song"}},
				"name":   map[string]any{"type": "string", "minLength": 1},
				"artist": map[string]any{"type": "string", "minLength": 1, "description": "同名楽曲を歌手で限定する場合だけ指定"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
			"required": []any{"kind", "name"},
		},
	}
}

func (r *ToolRunner) executeMusicCatalogLookupV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	if r.config.MusicCatalogLookup == nil {
		return tool.NewError(tool.ErrNotFound, "music catalog lookup is unavailable", nil), nil
	}
	for key := range args {
		if key != "kind" && key != "name" && key != "artist" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "music_catalog.lookup contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	kind, ok := args["kind"].(string)
	if !ok || (kind != "artist" && kind != "song") {
		return tool.NewError(tool.ErrValidationFailed, "kind must be artist or song", map[string]any{"field": "kind"}), nil
	}
	name, ok := args["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return tool.NewError(tool.ErrValidationFailed, "name is required", map[string]any{"field": "name"}), nil
	}
	artist := ""
	if raw, exists := args["artist"]; exists {
		artist, ok = raw.(string)
		if !ok || strings.TrimSpace(artist) == "" || kind != "song" {
			return tool.NewError(tool.ErrValidationFailed, "artist is valid only for song lookup", map[string]any{"field": "artist"}), nil
		}
	}
	limit := 10
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > 20 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 20", map[string]any{"field": "limit"}), nil
		}
	}
	result, err := r.config.MusicCatalogLookup.LookupMusic(ctx, kind, name, artist, limit)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "music catalog indexed lookup failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}
