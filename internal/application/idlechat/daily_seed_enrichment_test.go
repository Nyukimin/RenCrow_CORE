package idlechat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type failingDailyBriefProvider struct {
	requests int
}

func (p *failingDailyBriefProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests++
	return llm.GenerateResponse{}, errors.New("provider unavailable")
}

func (p *failingDailyBriefProvider) Name() string { return "collection-test-shiro" }

type oneArticleAtATimeProvider struct {
	badURL                    string
	completedBeforeSecondItem bool
}

func (p *oneArticleAtATimeProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	prompt := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(prompt, p.badURL) {
		cache := getDailyCache()
		p.completedBeforeSecondItem = cache != nil && len(cache.NewsSeedItems) > 0 && cache.NewsSeedItems[0].SourceReadStatus == "ready"
		return llm.GenerateResponse{}, errors.New("2件目の処理失敗")
	}
	switch {
	case strings.Contains(prompt, "工程: 原文翻訳"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"1件目の原文を日本語に翻訳しました。"}]}`}, nil
	case strings.Contains(prompt, "工程: 用語抽出"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"terms":[]}]}`}, nil
	case strings.Contains(prompt, "工程: サマリと見解"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"summary":"1件目のサマリです。","perspective":"Shiroの見解: 1件目の見解です。"}]}`}, nil
	default:
		return llm.GenerateResponse{}, errors.New("想定外の工程です")
	}
}

func (p *oneArticleAtATimeProvider) Name() string { return "collection-test-worker" }

func TestEnrichCurrentDailySeedsPublishesJapaneseSkillOutput(t *testing.T) {
	fetchedAt := time.Date(2026, 7, 21, 4, 0, 0, 0, jst)
	articleURL := "https://example.com/articles/rag"
	definitionURL := "https://example.org/reference/rag"
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-21",
		NewsSeedItems: []NewsSeed{{
			Title: "RAG検索支援機能を発表", Category: "ai_frontier", Source: "公式ニュース", SourceType: "rss", URL: articleURL,
		}},
		FetchedAt: fetchedAt, EnrichmentStatus: "pending",
	})
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL:    {URL: articleURL, Text: "新しいRAG検索支援機能を提供します。LLMへの入力に検索資料を追加します。"},
			definitionURL: {URL: definitionURL, Text: "RAGは、検索した外部情報を生成モデルへの入力に加える手法です。"},
		},
		readErrors: map[string]error{},
		searchResults: map[string][]DailyTermSearchResult{
			"RAG": {{Title: "RAGの解説", URL: definitionURL}},
		},
		searchErrors: map[string]error{},
	}
	provider := &orderedDailyBriefProvider{events: &events}
	chatWorker := &failingDailyBriefProvider{}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": chatWorker, "worker": provider})
	orch.SetDailySourceBriefResearch(research)

	orch.enrichCurrentDailySeeds()

	got := getDailyCache()
	if got.EnrichmentStatus != "ready" || got.EnrichmentProvider != "collection-test-worker" || got.EnrichedAt.IsZero() {
		t.Fatalf("enrichment state = %+v", got)
	}
	if chatWorker.requests != 0 {
		t.Fatalf("日次情報収集でChatWorkerを呼んではなりません: %d", chatWorker.requests)
	}
	item := got.NewsSeedItems[0]
	if item.SourceReadStatus != "ready" || len(item.TermNotes) != 2 || item.Summary == "" || item.Perspective == "" {
		t.Fatalf("enriched item = %+v", item)
	}
}

func TestEnrichCurrentDailySeedsKeepsSafeFallbackOnProviderFailure(t *testing.T) {
	articleURL := "https://example.com/ai"
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-21",
		NewsSeedItems: []NewsSeed{{
			Title: "AI機能を発表", Category: "ai_frontier", Source: "公式ニュース", SourceType: "rss", URL: articleURL,
		}},
		FetchedAt: time.Now(), EnrichmentStatus: "pending",
	})
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL: {URL: articleURL, Text: "AI機能を発表しました。"},
		},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}
	provider := &failingDailyBriefProvider{}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": provider})
	orch.SetDailySourceBriefResearch(research)

	orch.enrichCurrentDailySeeds()

	got := getDailyCache()
	if got.EnrichmentStatus != "fallback" || !strings.Contains(got.EnrichmentError, "provider unavailable") {
		t.Fatalf("fallback state = %+v", got)
	}
	item := got.NewsSeedItems[0]
	if item.SourceReadStatus != "unprocessed" || !strings.Contains(item.Summary, "処理を完了できませんでした") || len(item.TermNotes) == 0 || item.Perspective == "" {
		t.Fatalf("fallback annotations must never guess or be blank: %+v", item)
	}
}

func TestEnrichCurrentDailySeedsCompletesAndPublishesOneArticleBeforeStartingNext(t *testing.T) {
	firstURL := "https://example.com/first"
	secondURL := "https://example.com/second"
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-21",
		NewsSeedItems: []NewsSeed{
			{Title: "1件目", URL: firstURL},
			{Title: "2件目", URL: secondURL},
		},
		FetchedAt: time.Now(), EnrichmentStatus: "pending",
	})
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			firstURL:  {URL: firstURL, Text: "First article body for sequential processing."},
			secondURL: {URL: secondURL, Text: "Second article body that fails."},
		},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}
	provider := &oneArticleAtATimeProvider{badURL: secondURL}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": provider})
	orch.SetDailySourceBriefResearch(research)

	orch.enrichCurrentDailySeeds()

	got := getDailyCache()
	if got.EnrichmentStatus != "partial" {
		t.Fatalf("enrichment status = %q, want partial", got.EnrichmentStatus)
	}
	if !provider.completedBeforeSecondItem {
		t.Fatal("2件目を始める前に1件目をcacheへ完了公開する必要があります")
	}
	if got.NewsSeedItems[0].SourceReadStatus != "ready" || got.NewsSeedItems[0].TranslatedBody == "" {
		t.Fatalf("1件目の完了結果を保持する必要があります: %+v", got.NewsSeedItems[0])
	}
	if got.NewsSeedItems[1].SourceReadStatus != "unprocessed" {
		t.Fatalf("失敗した2件目だけfallbackにする必要があります: %+v", got.NewsSeedItems[1])
	}
}
