package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// executeWebSearch はWeb検索を実行（内部ヘルパー + Phase 4.2 KB自動保存）
func (m *MioAgent) executeWebSearch(ctx context.Context, query string) (string, error) {
	if m.toolRunner == nil {
		return "", fmt.Errorf("toolRunner not available")
	}

	// クエリから検索キーワードを抽出（不要な部分を除去）
	cleanedQuery := cleanSearchQuery(query)

	if m.searchCacheManager != nil {
		cachedResults, hit, err := m.searchCacheManager.GetFreshWebSearchCache(ctx, cleanedQuery)
		if err != nil {
			log.Printf("[Mio] Search cache lookup failed: %v", err)
		} else if hit {
			return formatWebSearchResults(cachedResults), nil
		}
	}

	args := map[string]interface{}{
		"query": cleanedQuery,
	}

	// V2ツールランナーで構造化データを取得
	toolResp, err := m.toolRunner.ExecuteV2(ctx, "web_search", args)
	if err != nil {
		return "", err
	}

	if toolResp.IsError() {
		return "", fmt.Errorf("%s", toolResp.Error.Message)
	}

	// 表示用の文字列結果
	result := toolResp.String()
	webResults := webSearchResultsFromMetadata(toolResp.Metadata)

	if m.searchCacheManager != nil && len(webResults) > 0 {
		if err := m.searchCacheManager.SaveWebSearchCache(ctx, cleanedQuery, webResults, 30*time.Minute); err != nil {
			log.Printf("[Mio] SaveWebSearchCache failed: %v", err)
		}
	}

	// Phase 4.2: KB自動保存（KBManager が設定されている場合）
	if m.kbManager != nil && len(webResults) > 0 {
		// KB保存（エラーはログのみ、検索結果は返す）
		domain := inferDomain(query) // クエリから domain を推定
		if err := m.kbManager.SaveWebSearchToKB(ctx, domain, cleanedQuery, webResults); err != nil {
			fmt.Printf("WARN: SaveWebSearchToKB failed: %v\n", err)
		}
	}

	return result, nil
}

func webSearchResultsFromMetadata(metadata map[string]any) []WebSearchResult {
	if metadata == nil || metadata["search_items"] == nil {
		return nil
	}
	b, err := json.Marshal(metadata["search_items"])
	if err != nil {
		return nil
	}
	var results []WebSearchResult
	if err := json.Unmarshal(b, &results); err != nil {
		return nil
	}
	return results
}

func formatWebSearchResults(results []WebSearchResult) string {
	if len(results) == 0 {
		return "検索結果が見つかりませんでした。"
	}
	var output strings.Builder
	output.WriteString("🔍 検索結果:\n\n")
	maxResults := 5
	if len(results) < maxResults {
		maxResults = len(results)
	}
	for i := 0; i < maxResults; i++ {
		item := results[i]
		output.WriteString(fmt.Sprintf("%d. %s\n", i+1, item.Title))
		output.WriteString(fmt.Sprintf("   %s\n", item.Snippet))
		output.WriteString(fmt.Sprintf("   %s\n\n", item.Link))
	}
	return output.String()
}

// cleanSearchQuery は検索クエリから不要な部分を除去
func cleanSearchQuery(query string) string {
	// 除去するパターン（質問形式の語尾など）
	removePatterns := []string{
		"について教えて", "を教えて", "教えて",
		"について調べて", "を調べて", "調べて",
		"について検索して", "を検索して", "について検索", "を検索", "検索して",
		"とは", "って何", "ってなに",
	}

	cleaned := query
	for _, pattern := range removePatterns {
		cleaned = strings.Replace(cleaned, pattern, "", -1)
	}

	return strings.TrimSpace(cleaned)
}

// needsWebSearch はGoogle Custom Search APIを使うWeb検索が必要かを判定する。
// API quota保護のため、時事・最新キーワードだけでは発火させず、明示的な検索/調査指示だけを許可する。
func needsWebSearch(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || looksLikeUserMemoryRecallQuestion(message) {
		return false
	}
	explicitKeywords := []string{
		"検索して",
		"検索",
		"調べて",
		"調査して",
		"webで",
		"Webで",
		"WEBで",
		"ウェブで",
		"ネットで",
		"インターネットで",
		"Googleで",
		"googleで",
	}
	for _, kw := range explicitKeywords {
		if strings.Contains(message, kw) {
			return true
		}
	}
	return false
}

func looksLikeUserMemoryRecallQuestion(message string) bool {
	selfMarkers := []string{"俺", "私", "僕", "ぼく", "わたし", "自分"}
	recallMarkers := []string{"知ってる", "覚えてる", "覚えていた", "覚えている", "記憶してる", "記憶している"}
	hasSelf := false
	for _, marker := range selfMarkers {
		if strings.Contains(message, marker) {
			hasSelf = true
			break
		}
	}
	if !hasSelf {
		return false
	}
	for _, marker := range recallMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
