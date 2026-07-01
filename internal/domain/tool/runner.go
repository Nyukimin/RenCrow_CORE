package tool

import "context"

// RunnerV2 は構造化レスポンスを返すツール実行インターフェース
type RunnerV2 interface {
	ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*ToolResponse, error)
	ListTools(ctx context.Context) ([]ToolMetadata, error)
}
