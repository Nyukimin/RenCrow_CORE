package conversation

import "context"

// ProfileExtractionResult はプロファイル抽出の結果
type ProfileExtractionResult struct {
	NewPreferences map[string]string `json:"preferences"`
	NewFacts       []string          `json:"facts"`
}

// HasData は抽出結果にデータがあるかを返す
func (r *ProfileExtractionResult) HasData() bool {
	return len(r.NewPreferences) > 0 || len(r.NewFacts) > 0
}

// ProfileExtractor はスレッド内の会話からユーザープロファイルを抽出する
type ProfileExtractor interface {
	Extract(ctx context.Context, thread *Thread, existing UserProfile) (*ProfileExtractionResult, error)
}
