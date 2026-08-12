package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func (r *ToolRunner) registerPersonRelatedCatalogCollectTool() {
	r.toolsV2["person_related_catalog.collect"] = r.executePersonRelatedCatalogCollectV2
}

func personRelatedCatalogCollectMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      "person_related_catalog.collect",
		Version:     "1.0.0",
		Category:    "mutation",
		Origin:      tool.OriginCoreRuntime,
		Description: "明示評価済み人物について、指定カテゴリの日本語関連作品収集をprovider sidecarへ一度だけ依頼し、検証済みartifactを保存する。",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"person_name": map[string]any{"type": "string", "minLength": 1},
				"category":    map[string]any{"type": "string", "enum": []any{"drama", "award", "music", "anime", "novel", "manga"}},
			},
			"required": []any{"person_name", "category"},
		},
	}
}

func (r *ToolRunner) executePersonRelatedCatalogCollectV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	if r.config.PersonRelatedCatalogCollector == nil {
		return tool.NewError(tool.ErrNotFound, "person related catalog collection is unavailable", nil), nil
	}
	for key := range args {
		if key != "person_name" && key != "category" {
			return tool.NewError(tool.ErrValidationFailed, "person_related_catalog.collect contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	personName, ok := args["person_name"].(string)
	if !ok || strings.TrimSpace(personName) == "" {
		return tool.NewError(tool.ErrValidationFailed, "person_name is required", map[string]any{"field": "person_name"}), nil
	}
	category, ok := args["category"].(string)
	if !ok || !validPersonRelatedCatalogCollectCategory(category) {
		return tool.NewError(tool.ErrValidationFailed, "category must be drama, award, music, anime, novel, or manga", map[string]any{"field": "category"}), nil
	}
	result, err := r.config.PersonRelatedCatalogCollector.Collect(ctx, personName, category)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "person related catalog collection failed", map[string]any{"category": category}), nil
	}
	return tool.NewSuccess(result), nil
}

func validPersonRelatedCatalogCollectCategory(category string) bool {
	switch category {
	case "drama", "award", "music", "anime", "novel", "manga":
		return true
	default:
		return false
	}
}
