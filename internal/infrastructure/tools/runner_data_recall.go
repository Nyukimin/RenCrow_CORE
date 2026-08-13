package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	dataRecallToolName                            = "data.recall"
	dataRecallUnavailableErrorCode tool.ErrorCode = "UNAVAILABLE"
	dataRecallDefaultLimit                        = 10
	dataRecallMaxLimit                            = 50
)

// DataRecallRequest is the complete model-controlled request sent to an
// operational recall provider. Authentication, actor and data scope are
// intentionally carried only by the trusted ToolExecutionScope context.
type DataRecallRequest struct {
	Store     string
	Operation string
	Query     string
	Limit     int
}

func (r *ToolRunner) registerDataRecallTool() {
	r.toolsV2[dataRecallToolName] = r.executeDataRecallV2
}

func dataRecallMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      dataRecallToolName,
		Version:     "1.0.0",
		Category:    "query",
		DryRun:      true,
		Origin:      tool.OriginCoreRuntime,
		Description: "Workerが認証済み実行scope内の名前付き運用データを読み取り専用で想起する。",
		Invariants: []string{
			"trusted ToolExecutionScope is required and must identify an Agent",
			"tool arguments never select user, authentication, data scope, database path, or SQL",
			"the provider owns per-store scope and operation policy",
		},
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"store":     map[string]any{"type": "string", "minLength": 1},
				"operation": map[string]any{"type": "string", "minLength": 1},
				"query":     map[string]any{"type": "string", "minLength": 1},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": dataRecallMaxLimit},
			},
			"required": []any{"store", "operation", "query"},
		},
	}
}

func (r *ToolRunner) executeDataRecallV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return tool.NewError(tool.ErrValidationFailed, "trusted Tool execution scope is missing", nil), nil
	}
	if err := scope.Validate(); err != nil || scope.ActorKind != tool.ActorKindAgent {
		return tool.NewError(tool.ErrValidationFailed, "trusted Tool execution scope is invalid", nil), nil
	}
	if r.config.OperationalDataRecall == nil {
		return tool.NewError(dataRecallUnavailableErrorCode, "operational data recall is unavailable", nil), nil
	}

	for key := range args {
		switch key {
		case "store", "operation", "query", "limit":
		default:
			return tool.NewError(tool.ErrValidationFailed, "data.recall contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	store, ok := dataRecallStringArgument(args, "store")
	if !ok || store == "" {
		return tool.NewError(tool.ErrValidationFailed, "store is required", map[string]any{"field": "store"}), nil
	}
	operation, ok := dataRecallStringArgument(args, "operation")
	if !ok || operation == "" {
		return tool.NewError(tool.ErrValidationFailed, "operation is required", map[string]any{"field": "operation"}), nil
	}
	query, ok := dataRecallStringArgument(args, "query")
	if !ok || query == "" {
		return tool.NewError(tool.ErrValidationFailed, "query is required", map[string]any{"field": "query"}), nil
	}
	limit := dataRecallDefaultLimit
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > dataRecallMaxLimit {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 50", map[string]any{"field": "limit"}), nil
		}
	}

	result, err := r.config.OperationalDataRecall.Recall(ctx, DataRecallRequest{
		Store:     store,
		Operation: operation,
		Query:     query,
		Limit:     limit,
	})
	if err != nil {
		return tool.NewError(dataRecallUnavailableErrorCode, "operational data recall is unavailable", nil), nil
	}
	return tool.NewSuccess(result), nil
}

func dataRecallStringArgument(args map[string]any, key string) (string, bool) {
	raw, exists := args[key]
	if !exists {
		return "", false
	}
	value, ok := raw.(string)
	return strings.TrimSpace(value), ok
}
