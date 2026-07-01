package conversation

import (
	"context"
	"fmt"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// StubConversationManager はPhase 1用のスタブ実装
// 全てのメソッドはno-op（何もしない）または空を返す
type StubConversationManager struct{}

// NewStubConversationManager は新しいStubConversationManagerを生成
func NewStubConversationManager() *StubConversationManager {
	return &StubConversationManager{}
}

func (s *StubConversationManager) Recall(ctx context.Context, sessionID string, query string, topK int) ([]conversation.Message, error) {
	// Phase 1: 常に空を返す
	return []conversation.Message{}, nil
}

func (s *StubConversationManager) Store(ctx context.Context, sessionID string, msg conversation.Message) error {
	// Phase 1: no-op
	return nil
}

func (s *StubConversationManager) FlushThread(ctx context.Context, threadID int64) (*conversation.ThreadSummary, error) {
	// Phase 1: 未実装エラー
	return nil, fmt.Errorf("FlushThread not implemented in stub")
}

func (s *StubConversationManager) IsNovelInformation(ctx context.Context, msg conversation.Message) (bool, float32, error) {
	// Phase 1: 常にfalse（新規情報なし）
	return false, 1.0, nil
}

func (s *StubConversationManager) GetActiveThread(ctx context.Context, sessionID string) (*conversation.Thread, error) {
	// Phase 1: 常にnil（Threadなし）
	return nil, conversation.ErrThreadNotFound
}

func (s *StubConversationManager) CreateThread(ctx context.Context, sessionID string, domain string) (*conversation.Thread, error) {
	// Phase 1: スタブThread生成（保存はしない）
	return conversation.NewThread(sessionID, domain), nil
}

func (s *StubConversationManager) GetAgentStatus(ctx context.Context, agentName string) (*conversation.AgentStatus, error) {
	// Phase 1: デフォルトステータス返却
	return conversation.NewAgentStatus(agentName), nil
}

func (s *StubConversationManager) UpdateAgentStatus(ctx context.Context, status *conversation.AgentStatus) error {
	// Phase 1: no-op
	return nil
}
