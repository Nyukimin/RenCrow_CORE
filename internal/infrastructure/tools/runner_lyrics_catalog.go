package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerLyricsCatalogLookupTool() {
	r.toolsV2["lyrics_catalog.lookup"] = r.executeLyricsCatalogLookupV2
}

func lyricsCatalogLookupMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID: "lyrics_catalog.lookup", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime,
		Description: "楽曲名を完全一致解決し、歌詞の権利状態、非復元Syntax特徴量、または許諾済み全文だけを照会する。",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"song":        map[string]any{"type": "string", "minLength": 1},
				"artist":      map[string]any{"type": "string", "minLength": 1},
				"language":    map[string]any{"type": "string", "minLength": 2},
				"information": map[string]any{"type": "string", "enum": []any{"rights", "syntax", "full_text"}},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
			"required": []any{"song", "information"},
		},
	}
}

func (r *ToolRunner) executeLyricsCatalogLookupV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	if r.config.LyricsCatalogLookup == nil {
		return tool.NewError(tool.ErrNotFound, "lyrics catalog lookup is unavailable", nil), nil
	}
	for key := range args {
		if key != "song" && key != "artist" && key != "language" && key != "information" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "lyrics_catalog.lookup contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	song, ok := args["song"].(string)
	if !ok || strings.TrimSpace(song) == "" {
		return tool.NewError(tool.ErrValidationFailed, "song is required", map[string]any{"field": "song"}), nil
	}
	information, ok := args["information"].(string)
	if !ok || (information != "rights" && information != "syntax" && information != "full_text") {
		return tool.NewError(tool.ErrValidationFailed, "information must be rights, syntax, or full_text", map[string]any{"field": "information"}), nil
	}
	artist, language := "", ""
	if raw, exists := args["artist"]; exists {
		artist, ok = raw.(string)
		if !ok || strings.TrimSpace(artist) == "" {
			return tool.NewError(tool.ErrValidationFailed, "artist must be a non-empty string", map[string]any{"field": "artist"}), nil
		}
	}
	if raw, exists := args["language"]; exists {
		language, ok = raw.(string)
		if !ok || len(strings.TrimSpace(language)) < 2 {
			return tool.NewError(tool.ErrValidationFailed, "language must be a language tag", map[string]any{"field": "language"}), nil
		}
	}
	limit := 10
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > 20 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 20", map[string]any{"field": "limit"}), nil
		}
	}
	result, err := r.config.LyricsCatalogLookup.LookupLyrics(ctx, song, artist, language, information, limit)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "lyrics catalog indexed lookup failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}
