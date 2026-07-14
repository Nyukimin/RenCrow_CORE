package agent

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// Classifier はタスク分類器のインターフェース
type Classifier interface {
	Classify(ctx context.Context, t task.Task) (routing.Decision, error)
}

// RuleDictionary はルール辞書のインターフェース
type RuleDictionary interface {
	Match(t task.Task) (routing.Route, float64, bool) // ルート, 確信度, マッチしたか
}

// ToolRunner はツール実行のインターフェース
type ToolRunner interface {
	ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) // Phase 4.2: 構造化レスポンス
	ListTools(ctx context.Context) ([]tool.ToolMetadata, error)
}

type AdvisorService interface {
	RequestAdvice(ctx context.Context, req advisor.AdviceRequest) (advisor.AdviceResult, error)
}

type AgentPolicyService interface {
	Decide(agentID string, action string) (agentprofile.PolicyDecision, error)
}

// MCPClient はMCPクライアントのインターフェース
type MCPClient interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error)
	ListTools(ctx context.Context, serverName string) ([]string, error)
}
