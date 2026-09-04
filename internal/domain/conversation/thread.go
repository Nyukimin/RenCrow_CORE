package conversation

import (
	"errors"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ThreadSeq = modulecore.ThreadSeq
type ThreadKind = modulecore.ThreadKind

const (
	ThreadKindUserConversation = modulecore.ThreadKindUserConversation
	ThreadKindAgentDiscussion  = modulecore.ThreadKindAgentDiscussion
	ThreadKindIdleChat         = modulecore.ThreadKindIdleChat
	ThreadKindDocument         = modulecore.ThreadKindDocument
	ThreadKindSystem           = modulecore.ThreadKindSystem
)

// ThreadStatus はThreadの状態
type ThreadStatus string

const (
	ThreadActive   ThreadStatus = "active"
	ThreadClosed   ThreadStatus = "closed"
	ThreadArchived ThreadStatus = "archived"
)

// Thread は「話題のまとまり」（6〜8ターン相当）
type Thread struct {
	ID         modulecore.ThreadID `json:"thread_id"`
	ThreadSeq  ThreadSeq           `json:"thread_seq"`
	ThreadKind ThreadKind          `json:"thread_kind"`
	SessionID  string              `json:"session_id"`
	Domain     string              `json:"domain"`
	Turns      []Message           `json:"turns"`
	Targets    []string            `json:"targets"`
	Cooldown   map[string]int      `json:"ct"`
	StartTime  time.Time           `json:"ts_start"`
	EndTime    *time.Time          `json:"ts_end,omitempty"`
	Status     ThreadStatus        `json:"status"`
}

// NewThread は新しいThreadを生成
func NewThread(sessionID string, domain string, kind ThreadKind, seq ThreadSeq) (*Thread, error) {
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return nil, err
	}
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	if err := seq.Validate(); err != nil {
		return nil, err
	}
	return &Thread{
		ID:         modulecore.NewThreadID(),
		ThreadSeq:  seq,
		ThreadKind: kind,
		SessionID:  sessionID,
		Domain:     domain,
		Turns:      make([]Message, 0, 12),
		Targets:    []string{},
		Cooldown:   make(map[string]int),
		StartTime:  time.Now(),
		Status:     ThreadActive,
	}, nil
}

// AddMessage はThreadにMessageを追加（最大12件保持）
func (t *Thread) AddMessage(msg Message) error {
	if t == nil {
		return errors.New("thread is required")
	}
	if t.Status != ThreadActive {
		return ErrInvalidThreadStatus
	}
	t.Turns = append(t.Turns, msg)
	if len(t.Turns) > 12 {
		t.Turns = t.Turns[len(t.Turns)-12:]
	}
	return nil
}

// LastMessageTime は最後のメッセージのタイムスタンプを返す
func (t *Thread) LastMessageTime() time.Time {
	if len(t.Turns) == 0 {
		return t.StartTime
	}
	return t.Turns[len(t.Turns)-1].Timestamp
}

// RecentMessagesText は直近 n 件のメッセージをテキスト連結して返す
func (t *Thread) RecentMessagesText(n int) string {
	if len(t.Turns) == 0 {
		return ""
	}
	start := len(t.Turns) - n
	if start < 0 {
		start = 0
	}
	var parts []string
	for _, m := range t.Turns[start:] {
		parts = append(parts, m.Msg)
	}
	return strings.Join(parts, " ")
}

// Close はThreadを終了
func (t *Thread) Close() error {
	if t == nil {
		return errors.New("thread is required")
	}
	if t.Status != ThreadActive {
		return ErrInvalidThreadStatus
	}
	now := time.Now()
	t.EndTime = &now
	t.Status = ThreadClosed
	return nil
}
