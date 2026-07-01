package conversation

import "context"

// ConversationEngine は Mio の「記憶脳」
// ConversationManager をラップし、RecallPack 生成と Thread 管理を提供
type ConversationEngine interface {
	// BeginTurn はターン開始時に呼び出し、RecallPack を返す
	BeginTurn(ctx context.Context, sessionID string, userMessage string) (*RecallPack, error)

	// EndTurn はターン終了時に呼び出し、メッセージ保存を実行
	EndTurn(ctx context.Context, sessionID string, userMessage string, response string) error

	// GetPersona は現在のペルソナ設定を返す
	GetPersona() PersonaState

	// FlushCurrentThread は現在のスレッドを強制フラッシュする（/compact 用）
	FlushCurrentThread(ctx context.Context, sessionID string) error

	// GetStatus は会話セッションの現在状態を返す（/status 用）
	GetStatus(ctx context.Context, sessionID string) (*ConversationStatus, error)

	// ResetSession はセッションをリセットする（/new 用）
	ResetSession(ctx context.Context, sessionID string) error
}
