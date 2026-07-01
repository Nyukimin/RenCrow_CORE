package conversation

import "time"

// SessionConversation はプロセス再起動や割り込み復帰に必要なまとまり
// 既存の domain/session.Session とは別のv5用Session
type SessionConversation struct {
	ID           string          `json:"session_id"`
	UserID       string          `json:"user_id"`
	History      []ThreadSummary `json:"history"`
	Agenda       string          `json:"agenda"`
	LastThreadID int64           `json:"last_thread_id"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// NewSessionConversation は新しいSessionConversationを生成
func NewSessionConversation(id string, userID string) *SessionConversation {
	now := time.Now()
	return &SessionConversation{
		ID:        id,
		UserID:    userID,
		History:   make([]ThreadSummary, 0),
		CreatedAt: now,
		UpdatedAt: now,
	}
}
