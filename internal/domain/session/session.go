package session

import (
	"errors"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// ErrSessionNotFound はセッションが見つからない場合のエラー
var ErrSessionNotFound = errors.New("session not found")

// Session はユーザーセッションを表すエンティティ
// 日次カットオーバーで切り替わり、会話履歴とメモリを保持
type Session struct {
	id             string // 不透明なCanonical SessionID。日付やrouting情報を埋め込まない。
	logicalDate    string
	channelAddress ChannelAddress
	history        []task.Task            // 会話履歴
	memory         map[string]interface{} // セッションメモリ
	createdAt      time.Time              // セッション作成時刻
	updatedAt      time.Time              // 最終更新時刻
}

// ID はセッションIDを返す
func (s *Session) ID() string {
	return s.id
}

// LogicalDate returns the explicit calendar partition used to find a session.
func (s *Session) LogicalDate() string {
	return s.logicalDate
}

// ChannelAddress returns routing information kept outside the opaque SessionID.
func (s *Session) ChannelAddress() ChannelAddress {
	return s.channelAddress
}

// CreatedAt は作成時刻を返す
func (s *Session) CreatedAt() time.Time {
	return s.createdAt
}

// UpdatedAt は最終更新時刻を返す
func (s *Session) UpdatedAt() time.Time {
	return s.updatedAt
}

// AddTask はタスクを履歴に追加
func (s *Session) AddTask(t task.Task) {
	s.history = append(s.history, t)
	s.updatedAt = time.Now()
}

// GetHistory は会話履歴を返す
func (s *Session) GetHistory() []task.Task {
	return s.history
}

// GetRecentHistory は最近N件の履歴を返す
func (s *Session) GetRecentHistory(n int) []task.Task {
	if len(s.history) <= n {
		return s.history
	}
	return s.history[len(s.history)-n:]
}

// SetMemory はメモリに値を設定
func (s *Session) SetMemory(key string, value interface{}) {
	s.memory[key] = value
	s.updatedAt = time.Now()
}

// GetMemory はメモリから値を取得
func (s *Session) GetMemory(key string) (interface{}, bool) {
	value, ok := s.memory[key]
	return value, ok
}

// GetAllMemory はメモリのコピーを返す
func (s *Session) GetAllMemory() map[string]interface{} {
	result := make(map[string]interface{}, len(s.memory))
	for k, v := range s.memory {
		result[k] = v
	}
	return result
}

// ClearMemory はメモリをクリア
func (s *Session) ClearMemory() {
	s.memory = make(map[string]interface{})
	s.updatedAt = time.Now()
}

// HistoryCount は履歴の件数を返す
func (s *Session) HistoryCount() int {
	return len(s.history)
}
