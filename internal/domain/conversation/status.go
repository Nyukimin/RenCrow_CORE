package conversation

import (
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ConversationStatus は会話セッションの現在状態
type ConversationStatus struct {
	SessionID    string              `json:"session_id"`
	ThreadID     modulecore.ThreadID `json:"thread_id"`
	ThreadSeq    ThreadSeq           `json:"thread_seq"`
	ThreadKind   ThreadKind          `json:"thread_kind"`
	ThreadDomain string              `json:"thread_domain"`
	TurnCount    int                 `json:"turn_count"`
	ThreadStart  time.Time           `json:"thread_start"`
	ThreadStatus ThreadStatus        `json:"thread_status"`
}
