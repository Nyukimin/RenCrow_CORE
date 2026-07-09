package agent

import (
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// LightMemory はセッション単位のインメモリ短期会話履歴。
// プロセス再起動でリセット（意図的）。goroutine-safe。
type LightMemory struct {
	mu       sync.Mutex
	sessions map[string]*sessionBuffer // key: sessionID（LINE UserID 等）
	maxTurns int                       // セッションあたりの最大保持ターン数
}

type sessionBuffer struct {
	turns []turn
}

type turn struct {
	userMessage string
	response    string
	timestamp   time.Time
}

// NewLightMemory は新しい LightMemory を作成する。
// maxTurns はセッションあたりの保持ターン数上限（推奨: 3〜5）。
func NewLightMemory(maxTurns int) *LightMemory {
	return &LightMemory{
		sessions: make(map[string]*sessionBuffer),
		maxTurns: maxTurns,
	}
}

// Record は会話ターン（userMessage + response ペア）を記録する。
// maxTurns を超えた古いターンは FIFO で破棄される。
func (m *LightMemory) Record(sessionID, userMessage, response string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	buf, exists := m.sessions[sessionID]
	if !exists {
		buf = &sessionBuffer{turns: make([]turn, 0, m.maxTurns)}
		m.sessions[sessionID] = buf
	}

	buf.turns = append(buf.turns, turn{
		userMessage: userMessage,
		response:    response,
		timestamp:   time.Now(),
	})

	// FIFO で古いターンを削除
	if len(buf.turns) > m.maxTurns {
		buf.turns = buf.turns[len(buf.turns)-m.maxTurns:]
	}
}

// RecentMessages は指定セッションの直近ターンを llm.Message スライスとして返す。
// user/assistant の交互メッセージを返す。system prompt は含まない。
// セッションが存在しない場合は nil を返す。
func (m *LightMemory) RecentMessages(sessionID string) []llm.Message {
	m.mu.Lock()
	defer m.mu.Unlock()

	buf, exists := m.sessions[sessionID]
	if !exists || len(buf.turns) == 0 {
		return nil
	}

	messages := make([]llm.Message, 0, len(buf.turns)*2)
	for _, t := range buf.turns {
		messages = append(messages,
			llm.Message{Role: "user", Content: t.userMessage},
			llm.Message{Role: "assistant", Content: t.response},
		)
	}

	return messages
}

// Clear は指定セッションの履歴を削除する。
func (m *LightMemory) Clear(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

// ClearAll は全セッションの履歴を削除する（日次カットオーバー用）。
func (m *LightMemory) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = make(map[string]*sessionBuffer)
}
