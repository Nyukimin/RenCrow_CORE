package tools

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"strings"
)

func (r *ToolRunner) registerDataCapabilityTool() {
	r.toolsV2["data_capability.describe"] = r.executeDataCapabilityV2
}
func dataCapabilityMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{ToolID: "data_capability.describe", Version: "1.0.0", Category: "query", Origin: tool.OriginCoreRuntime, Description: "RenCrowで安全に照会できるデータ能力と利用状態を説明する。物理pathやDB内容は返さない。", Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"operation": map[string]any{"type": "string", "enum": []any{"list_catalog", "list_available", "describe"}}, "name": map[string]any{"type": "string", "minLength": 1}}, "required": []any{"operation"}}}
}
func (r *ToolRunner) executeDataCapabilityV2(_ context.Context, args map[string]any) (*tool.ToolResponse, error) {
	for key := range args {
		if key != "operation" && key != "name" {
			return tool.NewError(tool.ErrValidationFailed, "unsupported data capability field", map[string]any{"field": key}), nil
		}
	}
	op, ok := args["operation"].(string)
	if !ok || (op != "list_catalog" && op != "list_available" && op != "describe") {
		return tool.NewError(tool.ErrValidationFailed, "operation must be list_catalog, list_available or describe", nil), nil
	}
	name := ""
	if raw, exists := args["name"]; exists {
		name, ok = raw.(string)
		if !ok || strings.TrimSpace(name) == "" {
			return tool.NewError(tool.ErrValidationFailed, "name must be a non-empty string", nil), nil
		}
	}
	if (op == "describe") != (name != "") {
		return tool.NewError(tool.ErrValidationFailed, "name is required only for describe", nil), nil
	}
	result, err := r.config.DataCapabilityCatalog.Execute(op, name)
	if err != nil {
		return tool.NewError(tool.ErrNotFound, "data capability is unknown", nil), nil
	}
	return tool.NewSuccess(result), nil
}
