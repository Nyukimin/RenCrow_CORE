package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerMovieCatalogLookupTool() {
	r.toolsV2["movie_catalog.lookup"] = r.executeMovieCatalogLookupV2
}

func movieCatalogLookupMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      "movie_catalog.lookup",
		Version:     "1.0.0",
		Category:    "query",
		Origin:      tool.OriginCoreRuntime,
		Description: "RenCrow映画カタログを索引付き完全一致で照会する。役者名から人物と出演映画、映画名から作品と出演者を取得する。",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"kind":        map[string]any{"type": "string", "enum": []any{"person", "movie"}},
				"name":        map[string]any{"type": "string", "minLength": 1},
				"information": map[string]any{"type": "string", "enum": []any{"overview", "cast", "staff", "profile", "filmography"}},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
			"required": []any{"kind", "name"},
		},
	}
}

func (r *ToolRunner) executeMovieCatalogLookupV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	if r.config.MovieCatalogLookup == nil {
		return tool.NewError(tool.ErrNotFound, "movie catalog lookup is unavailable", nil), nil
	}
	for key := range args {
		if key != "kind" && key != "name" && key != "information" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "movie_catalog.lookup contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	kind, ok := args["kind"].(string)
	if !ok || (kind != "person" && kind != "movie") {
		return tool.NewError(tool.ErrValidationFailed, "kind must be person or movie", map[string]any{"field": "kind"}), nil
	}
	name, ok := args["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return tool.NewError(tool.ErrValidationFailed, "name is required", map[string]any{"field": "name"}), nil
	}
	information := ""
	if raw, exists := args["information"]; exists {
		information, ok = raw.(string)
		if !ok || !validMovieCatalogInformation(kind, information) {
			return tool.NewError(tool.ErrValidationFailed, "information is invalid for kind", map[string]any{"field": "information"}), nil
		}
	}
	limit := 0
	if raw, exists := args["limit"]; exists {
		number, ok := integerToolArgument(raw)
		if !ok || number < 1 || number > 20 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 20", map[string]any{"field": "limit"}), nil
		}
		limit = number
	}
	result, err := r.config.MovieCatalogLookup.Lookup(ctx, kind, name, information, limit)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "movie catalog indexed lookup failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}

func validMovieCatalogInformation(kind, information string) bool {
	if kind == "movie" {
		return information == "overview" || information == "cast" || information == "staff"
	}
	return information == "profile" || information == "filmography"
}

func integerToolArgument(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		if number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	default:
		return 0, false
	}
}
