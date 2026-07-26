package entity

import (
	"fmt"
	"sync/atomic"
	"time"
)

type GlossaryItem struct {
	ID          string    `json:"id"`
	Term        string    `json:"term"`
	Explanation string    `json:"explanation"`
	Source      string    `json:"source"`
	Category    string    `json:"category"` // e.g., "new_word", "organization", "location"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewGlossaryItem(term, explanation, source, category string) *GlossaryItem {
	now := time.Now()
	return &GlossaryItem{
		ID:          generateID(),
		Term:        term,
		Explanation: explanation,
		Source:      source,
		Category:    category,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// idSequence は生成するIDの一意性を担保する
var idSequence atomic.Uint64

// generateID は用語集エントリのIDを生成する
//
// ID は PRIMARY KEY であり、保存は INSERT OR REPLACE のため、衝突すると
// エラーにならず先行レコードを無言で上書きする。time.Now().UnixNano() だけでは
// クロック粒度が粗い環境（Windowsでは100ns〜1ms程度）で連続生成したIDが
// 衝突するため、単調増加カウンタを併用する。
func generateID() string {
	return fmt.Sprintf("gloss_%d_%d", time.Now().UnixNano(), idSequence.Add(1))
}
