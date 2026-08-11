package tools

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"strings"
)

func (r *ToolRunner) registerGlossaryTool() { r.toolsV2["glossary.lookup"] = r.executeGlossaryV2 }
func glossaryMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{ToolID: "glossary.lookup", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime, Description: "RenCrow用語集を完全一致索引で照会する。", Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []any{"define_term", "list_category"}}, "term": map[string]any{"type": "string", "minLength": 1}, "category": map[string]any{"type": "string", "minLength": 1}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}}, "required": []any{"operation"}}}
}
func (r *ToolRunner) executeGlossaryV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	for key := range args {
		if key != "operation" && key != "term" && key != "category" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "unsupported glossary field", map[string]any{"field": key}), nil
		}
	}
	op, ok := args["operation"].(string)
	if !ok || (op != "define_term" && op != "list_category") {
		return tool.NewError(tool.ErrValidationFailed, "invalid glossary operation", nil), nil
	}
	stringArg := func(key string) (string, bool) {
		raw, exists := args[key]
		if !exists {
			return "", true
		}
		value, valid := raw.(string)
		return strings.TrimSpace(value), valid
	}
	term, termOK := stringArg("term")
	category, catOK := stringArg("category")
	if !termOK || !catOK || (op == "define_term" && (term == "" || category != "")) || (op == "list_category" && (category == "" || term != "")) {
		return tool.NewError(tool.ErrValidationFailed, "operation requires exactly one matching lookup value", nil), nil
	}
	limit := 0
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > 20 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be 1 to 20", nil), nil
		}
	}
	result, err := r.config.GlossaryLookup.Lookup(ctx, op, term, category, limit)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "glossary indexed lookup failed", nil), nil
	}
	return tool.NewSuccess(result), nil
}
