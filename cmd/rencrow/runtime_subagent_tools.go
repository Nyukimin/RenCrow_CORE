package main

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	healthadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// buildHealthHandler は Health Check HTTP ハンドラを構築
func (d *Dependencies) buildHealthHandler(cfg *config.Config) *healthadapter.Handler {
	return healthadapter.NewHandler(buildHealthService(cfg))
}

// resolveSubagentProvider はRenCrow_LLM WorkerのToolCallingProviderを使用する。
func resolveSubagentProvider(_ *config.Config, fallback llm.ToolCallingProvider) llm.ToolCallingProvider {
	return fallback
}

// mustGetToolList はツールリストを取得（エラーは無視）
func mustGetToolList(runner tool.RunnerV2) []string {
	metas, _ := runner.ListTools(context.Background())
	list := make([]string, 0, len(metas))
	for _, meta := range metas {
		list = append(list, meta.ToolID)
	}
	return list
}
