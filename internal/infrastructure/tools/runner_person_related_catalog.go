package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerPersonRelatedCatalogLookupTool() {
	r.toolsV2["person_related_catalog.lookup"] = r.executePersonRelatedCatalogLookupV2
}

func personRelatedCatalogLookupMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      "person_related_catalog.lookup",
		Version:     "1.0.0",
		Category:    "query",
		Origin:      tool.OriginCoreRuntime,
		Description: "評価済み人物に紐づく日本語関連作品を、人物名の完全一致解決と索引付き照会で取得する。映画カテゴリは映画カタログの出演作品を返す。",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"person_name": map[string]any{"type": "string", "minLength": 1},
				"category":    map[string]any{"type": "string", "enum": []any{"movie", "drama", "award", "music", "anime", "novel", "manga"}},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			},
			"required": []any{"person_name", "category"},
		},
	}
}

func (r *ToolRunner) executePersonRelatedCatalogLookupV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	if r.config.PersonRelatedCatalogLookup == nil {
		return tool.NewError(tool.ErrNotFound, "person related catalog lookup is unavailable", nil), nil
	}
	for key := range args {
		if key != "person_name" && key != "category" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "person_related_catalog.lookup contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	personName, ok := args["person_name"].(string)
	if !ok || strings.TrimSpace(personName) == "" {
		return tool.NewError(tool.ErrValidationFailed, "person_name is required", map[string]any{"field": "person_name"}), nil
	}
	category, ok := args["category"].(string)
	if !ok || !validPersonRelatedCatalogCategory(category) {
		return tool.NewError(tool.ErrValidationFailed, "category is invalid", map[string]any{"field": "category"}), nil
	}
	limit := 20
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > 50 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 50", map[string]any{"field": "limit"}), nil
		}
	}
	result, err := r.config.PersonRelatedCatalogLookup.Lookup(ctx, personName, category, limit)
	if err != nil {
		var notFound *PersonRelatedCatalogNotFoundError
		if errors.As(err, &notFound) {
			return tool.NewError(tool.ErrNotFound, "person was not found in the movie catalog", map[string]any{"person_name": personName, "status": "not_found"}), nil
		}
		var ambiguous *PersonRelatedCatalogAmbiguousError
		if errors.As(err, &ambiguous) {
			return tool.NewError(tool.ErrConflict, "person name is ambiguous", map[string]any{"person_name": personName, "status": "ambiguous", "candidates": ambiguous.Candidates}), nil
		}
		return tool.NewError(tool.ErrInternalError, "person related catalog indexed lookup failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}

func validPersonRelatedCatalogCategory(category string) bool {
	switch category {
	case "movie", "drama", "award", "music", "anime", "novel", "manga":
		return true
	default:
		return false
	}
}
