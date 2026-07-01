package tools

import (
	"context"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// v2Wrap は V1 ToolFunc を V2 ToolFuncV2 に変換する
// V1 エラーが *ToolError を含む場合、そのコードを維持する
func v2Wrap(fn ToolFunc) ToolFuncV2 {
	return func(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
		result, err := fn(ctx, args)
		if err != nil {
			return classifyV1Error(err), nil
		}
		return tool.NewSuccess(result), nil
	}
}

// classifyV1Error は V1 エラーを適切な ErrorCode に分類する
func classifyV1Error(err error) *tool.ToolResponse {
	// ToolError がそのまま返された場合（ミドルウェアからのバリデーションエラー等）
	if te, ok := err.(*tool.ToolError); ok {
		return tool.NewError(te.Code, te.Message, te.Details)
	}

	// context.DeadlineExceeded → TIMEOUT
	if err == context.DeadlineExceeded {
		return tool.NewError(tool.ErrTimeout, err.Error(), nil)
	}

	// エラーメッセージによる分類
	msg := err.Error()
	switch {
	case strings.Contains(msg, "VALIDATION_FAILED"):
		return tool.NewError(tool.ErrValidationFailed, msg, nil)
	case strings.Contains(msg, "not found") || strings.Contains(msg, "no such file"):
		return tool.NewError(tool.ErrNotFound, msg, nil)
	case strings.Contains(msg, "permission denied"):
		return tool.NewError(tool.ErrPermissionDenied, msg, nil)
	case strings.Contains(msg, "timed out") || strings.Contains(msg, "deadline exceeded"):
		return tool.NewError(tool.ErrTimeout, msg, nil)
	default:
		return tool.NewError(tool.ErrInternalError, msg, nil)
	}
}

// ToolDefinitions はLLMに渡すツール定義一覧を返す
// subagentツールは再帰呼び出し防止のため除外する
func (r *ToolRunner) ToolDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.metadata))
	for name, m := range r.metadata {
		if name == "subagent" {
			continue // 再帰防止
		}
		if m.Description == "" {
			continue // 説明なしのツールはtool callingに含めない
		}
		defs = append(defs, llm.ToolDefinition{
			Type: "function",
			Function: llm.ToolFunctionDef{
				Name:        m.ToolID,
				Description: m.Description,
				Parameters:  m.Parameters,
			},
		})
	}
	return defs
}
