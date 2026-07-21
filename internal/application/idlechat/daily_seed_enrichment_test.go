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
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": provider})
	orch.SetDailySourceBriefResearch(research)

	orch.enrichCurrentDailySeeds()

	got := getDailyCache()
	if got.EnrichmentStatus != "ready" || got.EnrichmentProvider != "collection-test-shiro" || got.EnrichedAt.IsZero() {
		t.Fatalf("enrichment state = %+v", got)
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
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"shiro": provider})
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
