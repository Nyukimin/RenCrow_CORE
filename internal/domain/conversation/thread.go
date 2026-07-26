package conversation

import (
	"strings"
	"sync/atomic"
	"time"
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
	ID        int64          `json:"thread_id"`
	SessionID string         `json:"session_id"`
	Domain    string         `json:"domain"`
	Turns     []Message      `json:"turns"`
	Targets   []string       `json:"targets"`
	Cooldown  map[string]int `json:"ct"`
	StartTime time.Time      `json:"ts_start"`
	EndTime   *time.Time     `json:"ts_end,omitempty"`
	Status    ThreadStatus   `json:"status"`
}

// NewThread は新しいThreadを生成
func NewThread(sessionID string, domain string) *Thread {
	return &Thread{
		ID:        generateThreadID(),
		SessionID: sessionID,
		Domain:    domain,
		Turns:     make([]Message, 0, 12),
		Targets:   []string{},
		Cooldown:  make(map[string]int),
		StartTime: time.Now(),
		Status:    ThreadActive,
	}
}

// AddMessage はThreadにMessageを追加（最大12件保持）
func (t *Thread) AddMessage(msg Message) {
	t.Turns = append(t.Turns, msg)
	if len(t.Turns) > 12 {
		t.Turns = t.Turns[len(t.Turns)-12:]
	}
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
func (t *Thread) Close() {
	now := time.Now()
	t.EndTime = &now
	t.Status = ThreadClosed
}

// lastThreadID は生成済みThread IDの最大値を保持する
var lastThreadID atomic.Int64

// generateThreadID はユニークなThread IDを生成する
//
// time.Now().UnixNano() だけでは、クロック粒度が粗い環境（Windowsでは
// 100ns〜1ms程度）で連続生成したIDが衝突する。単調増加を保証することで、
// 時刻順序を維持したまま一意性を担保する。
func generateThreadID() int64 {
	now := time.Now().UnixNano()
	for {
		last := lastThreadID.Load()
		next := now
		if next <= last {
			next = last + 1
		}
		if lastThreadID.CompareAndSwap(last, next) {
			return next
		}
	}
}
