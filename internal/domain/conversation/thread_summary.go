package conversation

import "time"

// ThreadSummary はThread終了時に生成される要約
type ThreadSummary struct {
	ThreadID  int64     `json:"thread_id"`
	SessionID string    `json:"session_id"`
	Domain    string    `json:"domain"`
	Summary   string    `json:"summary"`
	Keywords  []string  `json:"keywords"`
	Roles     []string  `json:"roles,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
	StartTime time.Time `json:"ts_start"`
	EndTime   time.Time `json:"ts_end"`
	IsNovel   bool      `json:"is_novel"`
	Score     float32   `json:"score,omitempty"` // VectorDB類似度スコア（検索結果のみ）
}
