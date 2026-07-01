package agent

import (
	"strings"
)

// getStringField は map から文字列フィールドを安全に取得
func getStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// truncateLog はログ用に文字列を切り詰める
func truncateLog(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// inferDomain はクエリから適切な domain を推定する
func inferDomain(query string) string {
	query = strings.ToLower(query)

	// プログラミング関連
	programmingKeywords := []string{
		"プログラミング", "コード", "言語", "関数", "変数", "クラス",
		"python", "go", "rust", "javascript", "java", "c++",
		"アルゴリズム", "データ構造", "フレームワーク", "ライブラリ",
	}
	for _, kw := range programmingKeywords {
		if strings.Contains(query, kw) {
			return "programming"
		}
	}

	// エンターテイメント関連
	entertainmentKeywords := []string{
		"映画", "ドラマ", "アニメ", "漫画", "ゲーム", "音楽",
		"俳優", "声優", "監督", "アーティスト",
	}
	for _, kw := range entertainmentKeywords {
		if strings.Contains(query, kw) {
			return "entertainment"
		}
	}

	// 料理関連
	cookingKeywords := []string{
		"料理", "レシピ", "食材", "調理", "食べ物", "飲み物",
		"レストラン", "カフェ",
	}
	for _, kw := range cookingKeywords {
		if strings.Contains(query, kw) {
			return "cooking"
		}
	}

	// 科学・技術関連
	scienceKeywords := []string{
		"科学", "物理", "化学", "生物", "数学", "天文",
		"技術", "工学", "AI", "機械学習",
		"量子", "相対性", "宇宙", "素粒子", "エネルギー",
	}
	for _, kw := range scienceKeywords {
		if strings.Contains(query, kw) {
			return "science"
		}
	}

	// デフォルトは general
	return "general"
}
