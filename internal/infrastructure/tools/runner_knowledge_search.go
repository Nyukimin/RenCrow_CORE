package tools

import (
	"context"
	"strings"
	"time"

	knowledgememoryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const knowledgeSearchToolName = "knowledge.search"

func (r *ToolRunner) registerKnowledgeSearchTool() {
	r.toolsV2[knowledgeSearchToolName] = r.executeKnowledgeSearchV2
}

func knowledgeSearchMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      knowledgeSearchToolName,
		Version:     "1.0.0",
		Category:    "query",
		Origin:      tool.OriginCoreRuntime,
		Description: "レビュー済みKnowledge Memoryの固定索引だけを、認証済みscope内で検索する。",
		Invariants: []string{
			"scope is read from trusted ToolExecutionScope context",
			"tool arguments never select user, scope, database, SQL, or raw payload",
			"missing or invalid scope is blocked without public fallback",
		},
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query":       map[string]any{"type": "string", "minLength": 1, "maxLength": 160},
				"record_type": map[string]any{"type": "string", "enum": []any{"creative_knowledge", "news_knowledge"}},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
			},
			"required": []any{"query", "record_type"},
		},
	}
}

func (r *ToolRunner) executeKnowledgeSearchV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.KnowledgeMemorySearcher == nil || !r.config.KnowledgeMemorySearchReady {
		return tool.NewError(tool.ErrNotFound, "knowledge memory indexed search is unavailable", nil), nil
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return tool.NewError(tool.ErrToolExecutionScopeMissing, "trusted Tool execution scope is missing", nil), nil
	}
	if err := scope.Validate(); err != nil {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "trusted Tool execution scope is invalid", nil), nil
	}
	searchScope, userID, err := scope.SearchScope()
	if err != nil {
		return tool.NewError(tool.ErrToolExecutionScopeInvalid, "trusted Tool execution scope has no searchable permission", nil), nil
	}
	for key := range args {
		if key != "query" && key != "record_type" && key != "limit" {
			return tool.NewError(tool.ErrValidationFailed, "knowledge.search contains an unsupported field", map[string]any{"field": key}), nil
		}
	}
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return tool.NewError(tool.ErrValidationFailed, "query is required", map[string]any{"field": "query"}), nil
	}
	recordType, ok := args["record_type"].(string)
	if !ok || !validKnowledgeRecordType(recordType) {
		return tool.NewError(tool.ErrValidationFailed, "record_type must be a supported Knowledge record type", map[string]any{"field": "record_type"}), nil
	}
	limit := 0
	if raw, exists := args["limit"]; exists {
		limit, ok = integerToolArgument(raw)
		if !ok || limit < 1 || limit > 20 {
			return tool.NewError(tool.ErrValidationFailed, "limit must be an integer between 1 and 20", map[string]any{"field": "limit"}), nil
		}
	}
	request := knowledgememoryapp.SearchRequest{
		Scope:      knowledgememoryapp.SearchScope{Scope: searchScope, UserID: userID},
		Query:      strings.TrimSpace(query),
		RecordType: recordType,
		Limit:      limit,
	}
	if err := request.Validate(); err != nil {
		return tool.NewError(tool.ErrValidationFailed, "knowledge.search arguments are invalid", nil), nil
	}
	results, err := r.config.KnowledgeMemorySearcher.Search(ctx, request)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, "knowledge memory indexed search failed", nil), nil
	}
	return &tool.ToolResponse{
		Result:      results,
		GeneratedAt: time.Now(),
		Metadata: map[string]any{
			"execution_receipt": map[string]any{
				"request_id": scope.RequestID,
				"actor_kind": scope.ActorKind,
				"actor_id":   scope.ActorID,
				"tool":       knowledgeSearchToolName,
			},
		},
	}, nil
}

func validKnowledgeRecordType(recordType string) bool {
	switch recordType {
	case "creative_knowledge", "news_knowledge":
		return true
	default:
		return false
	}
}
