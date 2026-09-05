package logging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// SessionLogEntry は1ターン分の会話ログエントリ
type SessionLogEntry struct {
	Timestamp string            `json:"ts"`
	SessionID string            `json:"session_id"`
	Channel   string            `json:"channel"`
	Role      string            `json:"role"`  // "user" | "assistant"
	Route     string            `json:"route"` // "CHAT" | "CODE" etc. (assistantのみ)
	TaskID    modulecore.TaskID `json:"task_id,omitempty"`
	MessageID string            `json:"message_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Content   string            `json:"content"`
}

// SessionLogWriter はセッション別の会話ログをJSONLファイルに書き出す
type SessionLogWriter struct {
	baseDir string
	mu      sync.Mutex
}

// NewSessionLogWriter は指定ディレクトリ配下にセッションログを書くWriterを返す
// baseDir: ~/.rencrow/logs/sessions のようなパス
func NewSessionLogWriter(baseDir string) *SessionLogWriter {
	return &SessionLogWriter{baseDir: baseDir}
}

// WriteUser はユーザーメッセージを記録する
func (w *SessionLogWriter) WriteUser(sessionID, channel, content string) {
	w.WriteUserWithIdentity(sessionID, channel, "", "", content)
}

func (w *SessionLogWriter) WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string) {
	w.write(SessionLogEntry{
		Timestamp: now(),
		SessionID: sessionID,
		Channel:   channel,
		Role:      "user",
		MessageID: messageID,
		TraceID:   traceID,
		Content:   content,
	})
}

// WriteAssistant はアシスタント応答を記録する
func (w *SessionLogWriter) WriteAssistant(sessionID, channel, route, taskID, content string) {
	w.WriteAssistantWithIdentity(sessionID, channel, route, taskID, "", "", content)
}

func (w *SessionLogWriter) WriteAssistantWithIdentity(sessionID, channel, route, taskID, messageID, traceID, content string) {
	canonicalTaskID, err := modulecore.ParseTaskID(taskID)
	if err != nil {
		log.Printf("ERROR: session log assistant task_id invalid task_id=%q err=%v", taskID, err)
		return
	}
	w.write(SessionLogEntry{
		Timestamp: now(),
		SessionID: sessionID,
		Channel:   channel,
		Role:      "assistant",
		Route:     route,
		TaskID:    canonicalTaskID,
		MessageID: messageID,
		TraceID:   traceID,
		Content:   content,
	})
}

// write は1件を追記する
//
// docs/10_ログ仕様.md: 記録パスは書き込み失敗を無言で握りつぶさず error として
// 出力する。従来は MkdirAll と OpenFile の失敗を素の return で捨てており、
// 会話ログが1行も残らなくても気づけなかった。
func (w *SessionLogWriter) write(entry SessionLogEntry) {
	path := w.pathFor(entry.SessionID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("ERROR: session log directory create failed path=%s session_id=%s err=%v", filepath.Dir(path), entry.SessionID, err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("ERROR: session log open failed path=%s session_id=%s err=%v", path, entry.SessionID, err)
		return
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(entry); err != nil {
		log.Printf("ERROR: session log write failed path=%s session_id=%s err=%v", path, entry.SessionID, err)
	}
}

func (w *SessionLogWriter) pathFor(sessionID string) string {
	t := time.Now()
	month := t.Format("2006-01")
	date := t.Format("2006-01-02")
	safe := sanitizeID(sessionID)
	return filepath.Join(w.baseDir, month, fmt.Sprintf("session_%s_%s.jsonl", date, safe))
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func sanitizeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id) && i < 64; i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
