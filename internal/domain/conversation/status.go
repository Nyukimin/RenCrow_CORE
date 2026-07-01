package conversation

import "time"

// ConversationStatus は会話セッションの現在状態
type ConversationStatus struct {
	SessionID    string       `json:"session_id"`
	ThreadID     int64        `json:"thread_id"`
	ThreadDomain string       `json:"thread_domain"`
	TurnCount    int          `json:"turn_count"`
	ThreadStart  time.Time    `json:"thread_start"`
	ThreadStatus ThreadStatus `json:"thread_status"`
}
