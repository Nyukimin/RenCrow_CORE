package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type runtimeDailySourceBriefResearch struct {
	fetcher tools.WebGatherFetcher
	search  domaintool.RunnerV2
}

const runtimeDailySourceBriefMinimumChars = 120

func newRuntimeDailySourceBriefResearch(fetcher tools.WebGatherFetcher, search domaintool.RunnerV2) idlechat.DailySourceBriefResearch {
	if fetcher == nil || search == nil {
		return nil
	}
	return &runtimeDailySourceBriefResearch{fetcher: fetcher, search: search}
}

func (r *runtimeDailySourceBriefResearch) ReadURL(ctx context.Context, rawURL string) (idlechat.DailySourceDocument, error) {
	if r == nil || r.fetcher == nil {
		return idlechat.DailySourceDocument{}, fmt.Errorf("本文取得機能が設定されていません")
	}
	req := modulewebgather.FetchRequest{
		URL: rawURL, Namespace: "daily-source-brief", SourceID: "daily-source-brief",
		FetchProvider: modulewebgather.DefaultFetchProvider, Extractor: modulewebgather.DefaultExtractor,
		StoreStaging: false, StoreStagingSet: true, Refresh: true,
		LicenseNote: modulewebgather.DefaultLicenseNote, Policy: modulewebgather.DefaultFetchPolicy(),
	}
	resp, err := r.fetcher.FetchURL(ctx, req)
	if err != nil {
		if delay := dailySourceBriefRetryDelay(resp.Diagnostics); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return idlechat.DailySourceDocument{}, ctx.Err()
			case <-timer.C:
			}
			resp, err = r.fetcher.FetchURL(ctx, req)
		}
	}
	if err != nil {
		return idlechat.DailySourceDocument{}, fmt.Errorf("URL本文を取得できませんでした: %w", err)
	}
	if strings.TrimSpace(resp.ExtractedText) == "" {
		return idlechat.DailySourceDocument{}, fmt.Errorf("URL本文が空です")
	}
	if len([]rune(strings.TrimSpace(resp.ExtractedText))) < runtimeDailySourceBriefMinimumChars {
		return idlechat.DailySourceDocument{}, fmt.Errorf("URL本文が短すぎるため、内容を確認できません")
	}
	return idlechat.DailySourceDocument{
		URL: firstRuntimeValue(resp.FinalURL, resp.URL, rawURL), Title: strings.TrimSpace(resp.Title), Text: strings.TrimSpace(resp.ExtractedText),
	}, nil
}

func (r *runtimeDailySourceBriefResearch) SearchTerm(ctx context.Context, term, query string) ([]idlechat.DailyTermSearchResult, error) {
	if r == nil || r.search == nil {
		return nil, fmt.Errorf("不明語検索機能が設定されていません")
	}
	resp, err := r.search.ExecuteV2(ctx, "web_search", map[string]any{"query": strings.TrimSpace(query)})
	if err != nil {
		return nil, fmt.Errorf("用語「%s」の検索に失敗しました: %w", term, err)
	}
	if resp == nil || resp.IsError() {
		message := "検索結果を取得できませんでした"
		if resp != nil && resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
			message = resp.Error.Message
		}
		return nil, fmt.Errorf("用語「%s」の検索に失敗しました: %s", term, message)
	}
	payload, err := json.Marshal(resp.Metadata["search_items"])
	if err != nil {
		return nil, fmt.Errorf("用語「%s」の検索結果を解析できません: %w", term, err)
	}
	var items []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal(payload, &items); err != nil {
		return nil, fmt.Errorf("用語「%s」の検索結果を解析できません: %w", term, err)
	}
	results := make([]idlechat.DailyTermSearchResult, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Link) == "" {
			continue
		}
		results = append(results, idlechat.DailyTermSearchResult{Title: strings.TrimSpace(item.Title), URL: strings.TrimSpace(item.Link), Snippet: strings.TrimSpace(item.Snippet)})
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("用語「%s」の参照候補URLが見つかりませんでした", term)
	}
	return results, nil
}

func dailySourceBriefRetryDelay(diagnostics map[string]any) time.Duration {
	if diagnostics == nil {
		return 0
	}
	var milliseconds int64
	switch value := diagnostics["retry_after_ms"].(type) {
	case int:
		milliseconds = int64(value)
	case int64:
		milliseconds = value
	case float64:
		milliseconds = int64(value)
	case json.Number:
		milliseconds, _ = value.Int64()
	}
	if milliseconds <= 0 {
		return 0
	}
	delay := time.Duration(milliseconds)*time.Millisecond + 50*time.Millisecond
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func firstRuntimeValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
