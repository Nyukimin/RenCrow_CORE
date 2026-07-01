package conversation

import "context"

// ConversationManager は会話管理の抽象化インターフェース
// Phase 1ではインターフェースのみ定義、Phase 2以降で実装
type ConversationManager interface {
	// Recall は想起（過去の会話を検索してRecallPackを生成）
	Recall(ctx context.Context, sessionID string, query string, topK int) ([]Message, error)

	// Store はメッセージを記憶
	Store(ctx context.Context, sessionID string, msg Message) error

	// FlushThread はThreadを終了し、要約を中期記憶へ保存
	FlushThread(ctx context.Context, threadID int64) (*ThreadSummary, error)

	// IsNovelInformation は新規情報かを判定（類似度 < 0.8）
	IsNovelInformation(ctx context.Context, msg Message) (bool, float32, error)

	// GetActiveThread はアクティブなThreadを取得
	GetActiveThread(ctx context.Context, sessionID string) (*Thread, error)

	// CreateThread は新しいThreadを作成
	CreateThread(ctx context.Context, sessionID string, domain string) (*Thread, error)

	// GetAgentStatus はAgent状態を取得
	GetAgentStatus(ctx context.Context, agentName string) (*AgentStatus, error)

	// UpdateAgentStatus はAgent状態を更新
	UpdateAgentStatus(ctx context.Context, status *AgentStatus) error
}
