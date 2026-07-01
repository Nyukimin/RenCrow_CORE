package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionLogEntry は1ターン分の会話ログエントリ
type SessionLogEntry struct {
	Timestamp string `json:"ts"`
	SessionID string `json:"session_id"`
	Channel   string `json:"channel"`
	Role      string `json:"role"`    // "user" | "assistant"
	Route     string `json:"route"`   // "CHAT" | "CODE" etc. (assistantのみ)
	JobID     string `json:"job_id,omitempty"`
	Content   string `json:"content"`
}

// SessionLogWriter はセッション別の会話ログをJSONLファイルに書き出す
type SessionLogWriter struct {
	baseDir string
	mu      sync.Mutex
}

// NewSessionLogWriter は指定ディレクトリ配下にセッションログを書くWriterを返す
// baseDir: ~/.picoclaw/logs/sessions のようなパス
func NewSessionLogWriter(baseDir string) *SessionLogWriter {
	return &SessionLogWriter{baseDir: baseDir}
}

// WriteUser はユーザーメッセージを記録する
func (w *SessionLogWriter) WriteUser(sessionID, channel, content string) {
	w.write(SessionLogEntry{
		Timestamp: now(),
		SessionID: sessionID,
		Channel:   channel,
		Role:      "user",
		Content:   content,
	})
}

// WriteAssistant はアシスタント応答を記録する
func (w *SessionLogWriter) WriteAssistant(sessionID, channel, route, jobID, content string) {
	w.write(SessionLogEntry{
		Timestamp: now(),
		SessionID: sessionID,
		Channel:   channel,
		Role:      "assistant",
		Route:     route,
		JobID:     jobID,
		Content:   content,
	})
}

func (w *SessionLogWriter) write(entry SessionLogEntry) {
	path := w.pathFor(entry.SessionID)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(entry)
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
