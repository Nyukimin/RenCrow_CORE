package conversation

import "time"

// Document は Knowledge Base（KB）に保存するドキュメント
type Document struct {
	ID        string                 `json:"id"`         // UUID
	Domain    string                 `json:"domain"`     // movie, tech, history等
	Content   string                 `json:"content"`    // 本文
	Source    string                 `json:"source"`     // URL、ファイルパス等
	Embedding []float32              `json:"embedding"`  // ベクトル（768次元）
	Meta      map[string]interface{} `json:"meta"`       // その他メタ情報
	CreatedAt time.Time              `json:"created_at"` // 作成日時
	UpdatedAt time.Time              `json:"updated_at"` // 更新日時
	Score     float32                `json:"score"`      // 検索時のスコア（類似度）
}

// IsValid はDocumentのバリデーション
func (d *Document) IsValid() bool {
	return d.ID != "" &&
		d.Domain != "" &&
		d.Content != "" &&
		len(d.Embedding) > 0
}
