package main

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	healthadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/health"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	infrahealth "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/health"
)

// buildHealthHandler は Health Check HTTP ハンドラを構築
func (d *Dependencies) buildHealthHandler(cfg *config.Config) *healthadapter.Handler {
	return healthadapter.NewHandler(buildHealthService(cfg, d.runtimeHealthChecks()...))
}

// runtimeHealthChecks are the health checks of what this process has open. The CLI
// health commands build the service without them: they open no store.
func (d *Dependencies) runtimeHealthChecks() []domainhealth.Check {
	var checks []domainhealth.Check
	if d != nil && d.actionStoreHealth != nil {
		checks = append(checks, infrahealth.NewActionStoreCheck(d.actionStoreHealth))
	}
	return checks
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
