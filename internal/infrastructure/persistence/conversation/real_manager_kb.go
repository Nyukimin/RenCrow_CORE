package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/vectordb"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/google/uuid"
)

// SaveWebSearchToKB はWeb検索結果をKnowledge Baseに保存
func (m *RealConversationManager) SaveWebSearchToKB(ctx context.Context, domain string, query string, results []WebSearchResult) error {
	if len(results) == 0 {
		return nil
	}

	if m.embedder == nil {
		log.Printf("SaveWebSearchToKB: Embedder not configured, skipping save (domain=%s, query=%q, %d results)", domain, query, len(results))
		return nil
	}

	successCount := 0
	var lastErr error

	// 各検索結果を Document に変換して保存
	for i, result := range results {
		// Content の Embedding 生成
		contentEmbedding, err := m.embedder.Embed(ctx, result.Title+" "+result.Snippet)
		if err != nil {
			log.Printf("SaveWebSearchToKB: Failed to embed result %d/%d (title=%q): %v", i+1, len(results), result.Title, err)
			lastErr = err
			continue
		}

		doc := &domconv.Document{
			ID:        uuid.New().String(),
			Domain:    domain,
			Content:   fmt.Sprintf("# %s\n\n%s\n\nSource: %s", result.Title, result.Snippet, result.Link),
			Source:    result.Link,
			Embedding: contentEmbedding,
			Meta: map[string]interface{}{
				"title":        result.Title,
				"snippet":      result.Snippet,
				"query":        query,
				"search_index": i,
			},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}

		// VectorDB保存をリトライ付きで実行
		err = withRetry(ctx, DefaultRetryConfig, func() error {
			return m.vectordbStore.SaveKB(ctx, doc)
		})
		if err != nil {
			log.Printf("SaveWebSearchToKB: Failed to save result %d/%d to VectorDB after retries (title=%q): %v", i+1, len(results), result.Title, err)
			lastErr = err
			continue
		}

		successCount++
	}

	// 一部でも成功していれば成功とみなす
	if successCount > 0 {
		log.Printf("SaveWebSearchToKB: Saved %d/%d results (domain=%s, query=%q)", successCount, len(results), domain, query)
		return nil
	}

	// 全て失敗した場合はエラーを返す
	if lastErr != nil {
		return fmt.Errorf("failed to save all %d web search results to KB (domain=%s, query=%q): %w", len(results), domain, query, lastErr)
	}

	return nil
}

func (m *RealConversationManager) SaveL1KnowledgeItem(ctx context.Context, item l1sqlite.L1KnowledgeItem) error {
	if m == nil || m.vectordbStore == nil || m.embedder == nil {
		return nil
	}
	content := strings.TrimSpace(item.SummaryDraft)
	if content == "" {
		content = strings.TrimSpace(item.RawText)
	}
	if content == "" {
		return nil
	}
	embedding, err := m.embedder.Embed(ctx, content)
	if err != nil {
		return fmt.Errorf("failed to generate l1 knowledge embedding: %w", err)
	}
	doc := &domconv.Document{
		ID:        item.ID,
		Domain:    item.Domain,
		Content:   content,
		Source:    item.SourceURL,
		Embedding: embedding,
		Meta: map[string]interface{}{
			"title":        item.Title,
			"source_id":    item.SourceID,
			"staging_id":   item.StagingID,
			"raw_hash":     item.RawHash,
			"license_note": item.LicenseNote,
			"keywords":     item.Keywords,
		},
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
	if err := withRetry(ctx, DefaultRetryConfig, func() error {
		return m.vectordbStore.SaveKB(ctx, doc)
	}); err != nil {
		return fmt.Errorf("failed to save l1 knowledge to vector db: %w", err)
	}
	return nil
}

func (m *RealConversationManager) GetFreshWebSearchCache(ctx context.Context, query string) ([]WebSearchResult, bool, error) {
	if m.l1Store == nil {
		return nil, false, nil
	}
	entry, err := m.l1Store.GetFreshSearchCache(ctx, "web", query, time.Now().UTC())
	if err != nil {
		return nil, false, err
	}
	if entry == nil {
		entry, err = m.l1Store.GetSimilarFreshSearchCache(ctx, "web", query, time.Now().UTC(), 0.75)
		if err != nil {
			return nil, false, err
		}
	}
	if entry == nil {
		return nil, false, nil
	}
	var results []WebSearchResult
	if err := json.Unmarshal([]byte(entry.ResultsJSON), &results); err != nil {
		return nil, false, fmt.Errorf("failed to unmarshal cached web search results: %w", err)
	}
	return results, true, nil
}

func (m *RealConversationManager) SaveWebSearchCache(ctx context.Context, query string, results []WebSearchResult, ttl time.Duration) error {
	if m.l1Store == nil || len(results) == 0 {
		return nil
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("failed to marshal web search cache results: %w", err)
	}
	sourceURLs := make([]string, 0, len(results))
	for _, result := range results {
		if result.Link != "" {
			sourceURLs = append(sourceURLs, result.Link)
		}
	}
	_, err = m.l1Store.SaveSearchCache(ctx, "web", query, string(resultsJSON), sourceURLs, ttl)
	return err
}

// WebSearchResult は agent.WebSearchResult のエイリアス（Phase 4.2）
type WebSearchResult = agent.WebSearchResult

// SearchKB はKnowledge Baseから関連ドキュメントを検索
func (m *RealConversationManager) SearchKB(ctx context.Context, domain string, query string, topK int) ([]*domconv.Document, error) {
	if m.embedder == nil {
		log.Printf("SearchKB: Embedder not configured, returning empty results (domain=%s, query=%q)", domain, query)
		return []*domconv.Document{}, nil
	}

	// Query の Embedding 生成
	queryEmbedding, err := m.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding for domain=%s, query=%q: %w", domain, query, err)
	}

	// VectorDB 検索をリトライ付きで実行
	var docs []*domconv.Document
	err = withRetry(ctx, DefaultRetryConfig, func() error {
		var searchErr error
		docs, searchErr = m.vectordbStore.SearchKB(ctx, domain, queryEmbedding, topK)
		return searchErr
	})
	if err != nil {
		return nil, fmt.Errorf("failed to search kb after retries (domain=%s, query=%q, topK=%d): %w", domain, query, topK, err)
	}

	return docs, nil
}

// ListKBDocuments はKBコレクション内の全ドキュメントを取得
func (m *RealConversationManager) ListKBDocuments(ctx context.Context, domain string, limit int) ([]*domconv.Document, error) {
	return m.vectordbStore.ListKBDocuments(ctx, domain, limit)
}

// GetKBCollections は存在するKBコレクション一覧を取得
func (m *RealConversationManager) GetKBCollections(ctx context.Context) ([]string, error) {
	return m.vectordbStore.GetKBCollections(ctx)
}

// GetKBStats はKBコレクションの統計情報を取得
func (m *RealConversationManager) GetKBStats(ctx context.Context, domain string) (*vectordb.KBStats, error) {
	return m.vectordbStore.GetKBStats(ctx, domain)
}

// DeleteOldKBDocuments は指定日時より古いKBドキュメントを削除
func (m *RealConversationManager) DeleteOldKBDocuments(ctx context.Context, domain string, before time.Time) (int, error) {
	return m.vectordbStore.DeleteOldKBDocuments(ctx, domain, before)
}
