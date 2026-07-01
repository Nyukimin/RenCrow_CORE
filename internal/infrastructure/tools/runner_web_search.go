package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
)

// executeWebSearch はWeb検索を実行（Google Custom Search JSON API使用）
func (r *ToolRunner) executeWebSearch(ctx context.Context, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("'query' argument is required and must be a string")
	}

	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query cannot be empty")
	}

	if r.config.WebSearchCache != nil {
		items, hit, err := r.config.WebSearchCache.GetFreshWebSearchCache(ctx, query)
		if err != nil {
			return "", fmt.Errorf("failed to read web search cache: %w", err)
		}
		if hit {
			return formatGoogleSearchResult(GoogleSearchResponse{Items: items}), nil
		}
	}

	// 設定チェック
	if r.config.GoogleAPIKey == "" || r.config.GoogleSearchEngineID == "" {
		return "", fmt.Errorf("Google Search API not configured")
	}

	// Google Custom Search JSON API
	apiURL := fmt.Sprintf("https://www.googleapis.com/customsearch/v1?key=%s&cx=%s&q=%s",
		r.config.GoogleAPIKey,
		r.config.GoogleSearchEngineID,
		url.QueryEscape(query))

	// HTTPクライアント（注入がなければデフォルトを使用）
	client := r.config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("search API returned status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// JSON解析
	var result GoogleSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if r.config.WebSearchCache != nil && len(result.Items) > 0 {
		if err := r.config.WebSearchCache.SaveWebSearchCache(ctx, query, result.Items, 30*time.Minute); err != nil {
			return "", fmt.Errorf("failed to save web search cache: %w", err)
		}
	}

	// 結果フォーマット
	return formatGoogleSearchResult(result), nil
}

// GoogleSearchResponse はGoogle Custom Search JSON APIレスポンス
type GoogleSearchResponse struct {
	Items             []GoogleSearchItem `json:"items"`
	SearchInformation struct {
		TotalResults string `json:"totalResults"`
	} `json:"searchInformation"`
}

// GoogleSearchItem は検索結果アイテム
type GoogleSearchItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

// formatGoogleSearchResult は検索結果を整形
func formatGoogleSearchResult(result GoogleSearchResponse) string {
	var output strings.Builder

	if len(result.Items) == 0 {
		return "検索結果が見つかりませんでした。"
	}

	output.WriteString("🔍 検索結果:\n\n")

	// 最大5件の検索結果を表示
	maxResults := 5
	if len(result.Items) < maxResults {
		maxResults = len(result.Items)
	}

	for i := 0; i < maxResults; i++ {
		item := result.Items[i]
		output.WriteString(fmt.Sprintf("%d. %s\n", i+1, item.Title))
		output.WriteString(fmt.Sprintf("   %s\n", item.Snippet))
		output.WriteString(fmt.Sprintf("   %s\n\n", item.Link))
	}

	return output.String()
}

// executeWebSearchV2 はWeb検索を実行し、構造化データ付きでToolResponseを返す
func (r *ToolRunner) executeWebSearchV2(ctx context.Context, args map[string]interface{}) (*tool.ToolResponse, error) {
	query, ok := args["query"].(string)
	if !ok {
		return tool.NewError(tool.ErrValidationFailed, "'query' argument is required and must be a string", nil), nil
	}

	if strings.TrimSpace(query) == "" {
		return tool.NewError(tool.ErrValidationFailed, "query cannot be empty", nil), nil
	}

	if r.config.WebSearchCache != nil {
		items, hit, err := r.config.WebSearchCache.GetFreshWebSearchCache(ctx, query)
		if err != nil {
			return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to read web search cache: %v", err), nil), nil
		}
		if hit {
			response := tool.NewSuccess(formatGoogleSearchResult(GoogleSearchResponse{Items: items}))
			response.Metadata = map[string]any{
				"query":        query,
				"search_items": items,
				"total_count":  len(items),
				"cache_hit":    true,
			}
			return response, nil
		}
	}

	// 設定チェック
	if r.config.GoogleAPIKey == "" || r.config.GoogleSearchEngineID == "" {
		return tool.NewError(tool.ErrNotFound, "Google Search API not configured", nil), nil
	}

	// Google Custom Search JSON API
	apiURL := fmt.Sprintf("https://www.googleapis.com/customsearch/v1?key=%s&cx=%s&q=%s",
		r.config.GoogleAPIKey,
		r.config.GoogleSearchEngineID,
		url.QueryEscape(query))

	// HTTPクライアント（注入がなければデフォルトを使用）
	client := r.config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to create request: %v", err), nil), nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to execute search: %v", err), nil), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return tool.NewError(tool.ErrInternalError, fmt.Sprintf("search API returned status %d: %s", resp.StatusCode, string(body[:min(len(body), 200)])), nil), nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to read response: %v", err), nil), nil
	}

	// JSON解析
	var result GoogleSearchResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to parse response: %v", err), nil), nil
	}
	if r.config.WebSearchCache != nil && len(result.Items) > 0 {
		if err := r.config.WebSearchCache.SaveWebSearchCache(ctx, query, result.Items, 30*time.Minute); err != nil {
			return tool.NewError(tool.ErrInternalError, fmt.Sprintf("failed to save web search cache: %v", err), nil), nil
		}
	}

	// 結果フォーマット（表示用文字列）
	formatted := formatGoogleSearchResult(result)

	// 構造化データをMetadataに格納
	metadata := map[string]any{
		"query":        query,
		"search_items": result.Items, // GoogleSearchItem の配列
		"total_count":  len(result.Items),
		"cache_hit":    false,
	}

	response := tool.NewSuccess(formatted)
	response.Metadata = metadata

	return response, nil
}
