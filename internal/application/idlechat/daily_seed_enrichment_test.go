package idlechat

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync/atomic"
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

type capturingDailySemanticProvider struct {
	requests []llm.GenerateRequest
}

func (p *capturingDailySemanticProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	prompt := req.Messages[len(req.Messages)-1].Content
	switch {
	case strings.Contains(prompt, "工程: 記事統合分析"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"原文を日本語へ忠実に翻訳した本文です。","terms":[{"term":"RAG","explanation":"本文だけでは意味を特定できません。","needs_lookup":true,"lookup_query":"RAG 公式 定義"}],"summary":"記事の要約です。","perspective":"Shiroの見解: 記事への見解です。"}]}`}, nil
	case strings.Contains(prompt, "工程: 原文翻訳"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"原文を日本語へ忠実に翻訳した本文です。"}]}`}, nil
	case strings.Contains(prompt, "工程: 用語抽出"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"terms":[{"term":"RAG","explanation":"本文だけでは意味を特定できません。","needs_lookup":true,"lookup_query":"RAG 公式 定義"}]}]}`}, nil
	case strings.Contains(prompt, "工程: 不明語補足"):
		return llm.GenerateResponse{Content: `{"items":[{"item_index":0,"term_index":0,"explanation":"検索した参照本文に基づく用語の説明です。"}]}`}, nil
	case strings.Contains(prompt, "工程: サマリと見解"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"summary":"記事の要約です。","perspective":"Shiroの見解: 記事への見解です。"}]}`}, nil
	default:
		return llm.GenerateResponse{}, errors.New("想定外の日次工程です")
	}
}

func (p *capturingDailySemanticProvider) Name() string { return "capturing-daily-semantic" }

type unexpectedDailyTranslationProvider struct {
	requests atomic.Int32
}

func (p *unexpectedDailyTranslationProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests.Add(1)
	return llm.GenerateResponse{}, errors.New("deterministic Japanese translation should bypass the provider")
}

func (p *unexpectedDailyTranslationProvider) Name() string { return "unexpected-daily-translation" }

func TestIsClearlyJapaneseDailySourceBodyUsesConservativeLetterRatio(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "clear Japanese", body: strings.Repeat("日本語の記事です。", 12), want: true},
		{name: "Chinese Han only", body: strings.Repeat("这是中文新闻正文", 8), want: false},
		{name: "English", body: strings.Repeat("English words ", 8), want: false},
		{name: "ambiguous mixed", body: strings.Repeat("日本語", 12) + strings.Repeat("English words ", 8), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClearlyJapaneseDailySourceBody(tt.body); got != tt.want {
				t.Fatalf("isClearlyJapaneseDailySourceBody() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTranslateDailySourceBodiesBypassesClearlyJapaneseBody(t *testing.T) {
	body := strings.Repeat("日本語", 12) + "\nそのまま保持します。"
	provider := &unexpectedDailyTranslationProvider{}
	inputs := []dailySourceBriefInput{{Index: 7, SourceURL: "https://example.com/japanese", Body: body}}

	translations, err := translateDailySourceBodies(context.Background(), provider, inputs)
	if err != nil {
		t.Fatalf("translateDailySourceBodies() error = %v", err)
	}
	if provider.requests.Load() != 0 {
		t.Fatalf("clear Japanese source must bypass the translation provider: requests=%d", provider.requests.Load())
	}
	if len(translations) != 1 || translations[0].Index != 7 || translations[0].TranslatedBody != body {
		t.Fatalf("translations = %#v, want unchanged body with original index", translations)
	}
}

func TestTranslateDailySourceBodiesUsesWorkerForEnglishAndAmbiguousMixedBodies(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "English", body: strings.Repeat("English words ", 8)},
		{name: "ambiguous mixed", body: strings.Repeat("日本語", 12) + strings.Repeat("English words ", 8)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &scriptedDailyOutputProvider{responses: []string{
				`{"items":[{"index":0,"translated_body":"Workerが翻訳した本文です。"}]}`,
			}}
			translations, err := translateDailySourceBodies(context.Background(), provider, []dailySourceBriefInput{{Index: 4, SourceURL: "https://example.com/article", Body: tt.body}})
			if err != nil {
				t.Fatalf("translateDailySourceBodies() error = %v", err)
			}
			if provider.requests.Load() != 1 {
				t.Fatalf("%s source must use Worker exactly once: requests=%d", tt.name, provider.requests.Load())
			}
			if len(translations) != 1 || translations[0].TranslatedBody != "Workerが翻訳した本文です。" {
				t.Fatalf("translations = %#v, want Worker output", translations)
			}
		})
	}
}

func TestTranslateDailySourceBodiesRestoresStableOrderAfterJapaneseBypass(t *testing.T) {
	provider := &scriptedDailyOutputProvider{responses: []string{
		`{"items":[{"index":0,"translated_body":"英語本文をWorkerで翻訳しました。"}]}`,
	}}
	inputs := []dailySourceBriefInput{
		{Index: 10, SourceURL: "https://example.com/ja-1", Body: strings.Repeat("日本語の記事です。", 12)},
		{Index: 20, SourceURL: "https://example.com/en", Body: strings.Repeat("English words ", 8)},
		{Index: 30, SourceURL: "https://example.com/ja-2", Body: strings.Repeat("漢字かな", 6)},
	}

	translations, err := translateDailySourceBodies(context.Background(), provider, inputs)
	if err != nil {
		t.Fatalf("translateDailySourceBodies() error = %v", err)
	}
	if provider.requests.Load() != 1 {
		t.Fatalf("only residual English input should reach Worker: requests=%d", provider.requests.Load())
	}
	want := []dailyTranslationItem{
		{Index: 10, TranslatedBody: inputs[0].Body},
		{Index: 20, TranslatedBody: "英語本文をWorkerで翻訳しました。"},
		{Index: 30, TranslatedBody: inputs[2].Body},
	}
	if len(translations) != len(want) {
		t.Fatalf("translations = %#v, want %#v", translations, want)
	}
	for index := range want {
		if translations[index] != want[index] {
			t.Fatalf("translations[%d] = %#v, want %#v", index, translations[index], want[index])
		}
	}
}

func TestAnalyzeDailySourceBodiesUsesOneRequestAndPreservesClearlyJapaneseBody(t *testing.T) {
	body := strings.Repeat("日本語の記事本文です。", 12)
	provider := &scriptedDailyOutputProvider{responses: []string{
		`{"items":[{"index":0,"translated_body":"LLMが返した値は採用しません。","terms":[],"summary":"記事本文の要約です。","perspective":"Shiroの見解: 記事本文を確認した見解です。"}]}`,
	}}
	analyses, err := analyzeDailySourceBodies(context.Background(), provider, []dailySourceBriefInput{{Index: 8, Body: body}})
	if err != nil {
		t.Fatalf("analyzeDailySourceBodies() error = %v", err)
	}
	if provider.requests.Load() != 1 {
		t.Fatalf("integrated analysis requests = %d, want 1", provider.requests.Load())
	}
	if len(analyses) != 1 || analyses[0].Index != 8 || analyses[0].TranslatedBody != body {
		t.Fatalf("analyses = %#v, want deterministic Japanese projection", analyses)
	}
}

func TestParseDailyArticleAnalysisRejectsMissingEnglishTranslation(t *testing.T) {
	_, err := parseDailyArticleAnalysis(
		`{"items":[{"index":0,"translated_body":"","terms":[],"summary":"記事の要約です。","perspective":"Shiroの見解: 見解です。"}]}`,
		[]dailySourceBriefInput{{Index: 0, Body: strings.Repeat("English article body. ", 8)}},
	)
	if err == nil || !strings.Contains(err.Error(), "翻訳") {
		t.Fatalf("parseDailyArticleAnalysis() error = %v, want translation validation error", err)
	}
}

func TestGenerateDailyBriefLLMLogsBoundedStageReceipt(t *testing.T) {
	provider := &capturingDailySemanticProvider{}
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)

	if _, err := generateDailyBriefLLM(context.Background(), provider, "translate_article", "工程: 原文翻訳"); err != nil {
		t.Fatalf("generateDailyBriefLLM() error = %v", err)
	}
	line := logs.String()
	for _, field := range []string{"purpose=translate_article", "input_runes=", "elapsed_ms=", "terminal=completed"} {
		if !strings.Contains(line, field) {
			t.Fatalf("stage receipt missing %q: %q", field, line)
		}
	}
}

func TestDailySemanticRequestsUseLowReasoningEffort(t *testing.T) {
	articleURL := "https://example.com/articles/daily-low-reasoning"
	definitionURL := "https://example.org/reference/daily-low-reasoning"
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL:    {URL: articleURL, Text: "RAGを使う記事本文です。"},
			definitionURL: {URL: definitionURL, Text: "RAGは検索情報を生成に加える手法です。"},
		},
		readErrors: map[string]error{},
		searchResults: map[string][]DailyTermSearchResult{
			"RAG": {{Title: "RAGの定義", URL: definitionURL}},
		},
		searchErrors: map[string]error{},
	}
	provider := &capturingDailySemanticProvider{}

	_, err := buildDailySourceBriefBatch(context.Background(), provider, research, []NewsSeed{{
		Title: "日次のlow reasoning検証", URL: articleURL, Source: "公式ニュース", SourceType: "rss",
	}})
	if err != nil {
		t.Fatalf("buildDailySourceBriefBatch: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("日次semantic request数 = %d, want 2 (統合分析 + 不明語補足)", len(provider.requests))
	}
	for index, req := range provider.requests {
		if req.ReasoningEffort != llm.ReasoningEffortLow {
			t.Errorf("request[%d] reasoning effort = %q, want %q", index, req.ReasoningEffort, llm.ReasoningEffortLow)
		}
	}
}

type oneArticleAtATimeProvider struct {
	badURL                    string
	completedBeforeSecondItem bool
}

type chatPriorityDailyBriefProvider struct {
	started       chan struct{}
	firstCanceled chan struct{}
	requests      atomic.Int32
	nonStreaming  atomic.Int32
}

func (p *chatPriorityDailyBriefProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if req.OnToken == nil {
		p.nonStreaming.Add(1)
	}
	requestNumber := p.requests.Add(1)
	if requestNumber == 1 {
		close(p.started)
		<-ctx.Done()
		close(p.firstCanceled)
		return llm.GenerateResponse{}, ctx.Err()
	}
	prompt := req.Messages[len(req.Messages)-1].Content
	switch {
	case strings.Contains(prompt, "工程: 記事統合分析"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"中断後に原文を分析しました。","terms":[],"summary":"再開後のサマリです。","perspective":"Shiroの見解: 対話を優先してから再開しました。"}]}`}, nil
	case strings.Contains(prompt, "工程: 原文翻訳"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"中断後に原文翻訳を再開しました。"}]}`}, nil
	case strings.Contains(prompt, "工程: 用語抽出"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"terms":[]}]}`}, nil
	case strings.Contains(prompt, "工程: サマリと見解"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"summary":"再開後のサマリです。","perspective":"Shiroの見解: 対話を優先してから再開しました。"}]}`}, nil
	default:
		return llm.GenerateResponse{}, errors.New("想定外の工程です")
	}
}

func (p *chatPriorityDailyBriefProvider) Name() string { return "chat-priority-worker" }

func (p *oneArticleAtATimeProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	prompt := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(prompt, p.badURL) {
		cache := getDailyCache()
		p.completedBeforeSecondItem = cache != nil && len(cache.NewsSeedItems) > 0 && cache.NewsSeedItems[0].SourceReadStatus == "ready"
		return llm.GenerateResponse{}, errors.New("2件目の処理失敗")
	}
	switch {
	case strings.Contains(prompt, "工程: 記事統合分析"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"1件目の原文を日本語に翻訳しました。","terms":[],"summary":"1件目のサマリです。","perspective":"Shiroの見解: 1件目の見解です。"}]}`}, nil
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
	if item.SourceReadStatus != "ready" || item.ProcessingStatus != "ready" || len(item.TermNotes) != 2 || item.Summary == "" || item.Perspective == "" {
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
	if item.SourceReadStatus != "ready" || item.ProcessingStatus != "translation_failed" || item.ProcessingError == "" {
		t.Fatalf("provider failure after source read must preserve source success and identify translation failure: %+v", item)
	}
	if item.TranslatedBody != "原文の取得は完了しましたが、翻訳に失敗しました。" || len(item.TermNotes) != 0 {
		t.Fatalf("原文取得失敗と翻訳失敗を区別する必要があります: %+v", item)
	}
}

func TestEnrichCurrentDailySeedsKeepsUnstartedItemsPendingWhenDependencyIsUnavailable(t *testing.T) {
	withDailySeedCache(t, &DailySeedCache{
		Date:          "2026-07-21",
		NewsSeedItems: []NewsSeed{{Title: "未着手の記事", URL: "https://example.com/pending", Summary: "RSSの未検証feed要約"}},
		FetchedAt:     time.Now(), EnrichmentStatus: "pending",
	})
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")

	orch.enrichCurrentDailySeeds()

	item := getDailyCache().NewsSeedItems[0]
	if item.SourceReadStatus != "unprocessed" || item.ProcessingStatus != "pending" || item.ProcessingError != "" {
		t.Fatalf("未着手項目はpendingとして残す必要があります: %+v", item)
	}
	if item.TranslatedBody != "原文取得・翻訳はまだ開始していません。" || item.Summary != "本文に基づく処理はまだ開始していません。" || len(item.TermNotes) != 0 {
		t.Fatalf("未着手を失敗文言や確認不能の用語で埋めてはいけません: %+v", item)
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
	if got.NewsSeedItems[1].SourceReadStatus != "ready" || got.NewsSeedItems[1].ProcessingStatus != "translation_failed" {
		t.Fatalf("失敗した2件目も原文取得成功と翻訳失敗を分離する必要があります: %+v", got.NewsSeedItems[1])
	}
}

func TestEnrichCurrentDailySeedsPausesForForegroundChatAndResumesWithoutLosingArticle(t *testing.T) {
	articleURL := "https://example.com/priority"
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-22",
		NewsSeedItems: []NewsSeed{{
			Title: "対話優先テスト", URL: articleURL,
		}},
		FetchedAt: time.Now(), EnrichmentStatus: "pending",
	})
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL: {URL: articleURL, Text: "Foreground chat must preempt this background enrichment request."},
		},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}
	provider := &chatPriorityDailyBriefProvider{started: make(chan struct{}), firstCanceled: make(chan struct{})}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": provider})
	orch.SetDailySourceBriefResearch(research)

	done := make(chan struct{})
	go func() {
		defer close(done)
		orch.enrichCurrentDailySeeds()
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("日次処理の最初のLLM requestが開始されませんでした")
	}
	orch.SetChatBusy(true)
	select {
	case <-provider.firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("foreground chat開始時に日次LLM requestがcancelされませんでした")
	}
	time.Sleep(100 * time.Millisecond)
	if requests := provider.requests.Load(); requests != 1 {
		t.Fatalf("foreground chat中に日次処理を再開してはいけません: requests=%d", requests)
	}

	orch.SetChatBusy(false)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("foreground chat終了後に日次処理が再開されませんでした")
	}

	got := getDailyCache()
	if got.EnrichmentStatus != "ready" || got.NewsSeedItems[0].TranslatedBody == "" || got.NewsSeedItems[0].Summary == "" {
		t.Fatalf("中断した記事を失わずに完了する必要があります: %+v", got)
	}
	if requests := provider.requests.Load(); requests != 2 {
		t.Fatalf("cancelされた統合分析1回と再開後の統合分析だけを実行する必要があります: requests=%d", requests)
	}
	if nonStreaming := provider.nonStreaming.Load(); nonStreaming != 0 {
		t.Fatalf("物理backendへcancelを伝播するため日次LLM requestはstreamingである必要があります: non_streaming=%d", nonStreaming)
	}
}

func TestDailyEnrichmentWaitsUntilForegroundIdleChatSessionEnds(t *testing.T) {
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	job := orch.beginDailyEnrichmentJob(time.Now())
	if job == nil {
		t.Fatal("daily enrichment job was not created")
	}
	defer orch.finishDailyEnrichmentJob(job)
	orch.mu.Lock()
	orch.chatActive = true
	orch.mu.Unlock()

	returned := make(chan error, 1)
	go func() { returned <- orch.waitForDailyEnrichmentWindow(job.ctx) }()
	select {
	case err := <-returned:
		t.Fatalf("Daily resumed while foreground IdleChat was active: %v", err)
	case <-time.After(75 * time.Millisecond):
	}

	orch.mu.Lock()
	orch.chatActive = false
	orch.mu.Unlock()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("waitForDailyEnrichmentWindow() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Daily did not resume after foreground IdleChat ended")
	}
}

type supersededDailyEnrichmentProvider struct {
	oldStarted  chan struct{}
	oldCanceled chan struct{}
	releaseOld  chan struct{}
	newStarted  chan struct{}
	newCanceled chan struct{}
	allowNew    chan struct{}
	oldOnce     atomic.Bool
	newOnce     atomic.Bool
}

func (p *supersededDailyEnrichmentProvider) Name() string { return "superseded-job-worker" }

func (p *supersededDailyEnrichmentProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	prompt := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(prompt, "old-article") && p.oldOnce.CompareAndSwap(false, true) {
		close(p.oldStarted)
		<-ctx.Done()
		close(p.oldCanceled)
		<-p.releaseOld
		return dailyEnrichmentTestResponse(prompt, "旧ジョブの結果")
	}
	if strings.Contains(prompt, "new-article") && p.newOnce.CompareAndSwap(false, true) {
		close(p.newStarted)
		select {
		case <-p.allowNew:
		case <-ctx.Done():
			close(p.newCanceled)
			return llm.GenerateResponse{}, ctx.Err()
		}
	}
	return dailyEnrichmentTestResponse(prompt, "新ジョブの結果")
}

func dailyEnrichmentTestResponse(prompt, label string) (llm.GenerateResponse, error) {
	switch {
	case strings.Contains(prompt, "工程: 記事統合分析"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"` + label + `の原文翻訳です。","terms":[],"summary":"` + label + `の要約です。","perspective":"Shiroの見解: ` + label + `の見解です。"}]}`}, nil
	case strings.Contains(prompt, "工程: 原文翻訳"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"` + label + `の原文翻訳です。"}]}`}, nil
	case strings.Contains(prompt, "工程: 用語抽出"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"terms":[]}]}`}, nil
	case strings.Contains(prompt, "工程: サマリと見解"):
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"summary":"` + label + `の要約です。","perspective":"Shiroの見解: ` + label + `の見解です。"}]}`}, nil
	default:
		return llm.GenerateResponse{}, errors.New("unknown daily enrichment stage")
	}
}

type retryableDailyError struct{ message string }

func (e retryableDailyError) Error() string   { return e.message }
func (e retryableDailyError) Retryable() bool { return true }

type retryableDailyProvider struct{ requests atomic.Int32 }

func (p *retryableDailyProvider) Name() string { return "retryable-daily-worker" }
func (p *retryableDailyProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.requests.Add(1) == 1 {
		return llm.GenerateResponse{}, retryableDailyError{message: "temporary structured generation failure"}
	}
	return dailyEnrichmentTestResponse(req.Messages[len(req.Messages)-1].Content, "再試行後")
}

type scriptedDailyOutputProvider struct {
	responses []string
	requests  atomic.Int32
}

func (p *scriptedDailyOutputProvider) Name() string { return "scripted-daily-output" }

func (p *scriptedDailyOutputProvider) Generate(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	index := int(p.requests.Add(1)) - 1
	if index >= len(p.responses) {
		return llm.GenerateResponse{}, errors.New("scripted daily output exhausted")
	}
	return llm.GenerateResponse{Content: p.responses[index]}, nil
}

func TestTranslateDailySourceBodiesRetriesGeneratedOutputInvalidOnce(t *testing.T) {
	provider := &scriptedDailyOutputProvider{responses: []string{
		`{"items":[{"index":0,"translated_body":"壊れた\q"}]}`,
		`{"items":[{"index":0,"translated_body":"再生成された翻訳です。"}]}`,
	}}
	inputs := []dailySourceBriefInput{{Index: 0, SourceURL: "https://example.com/article", Body: "article body"}}

	translations, err := translateDailySourceBodies(context.Background(), provider, inputs)
	if err != nil {
		t.Fatalf("translateDailySourceBodies() error = %v", err)
	}
	if provider.requests.Load() != 2 {
		t.Fatalf("generated-output-invalid should consume one regeneration: requests=%d, want 2", provider.requests.Load())
	}
	if len(translations) != 1 || translations[0].TranslatedBody != "再生成された翻訳です。" {
		t.Fatalf("translations = %#v, want regenerated valid output", translations)
	}
}

func TestTranslateDailySourceBodiesReturnsSecondGeneratedOutputInvalidAfterTwoCalls(t *testing.T) {
	invalid := `{"items":[{"index":0,"translated_body":"壊れた\q"}]}`
	provider := &scriptedDailyOutputProvider{responses: []string{invalid, invalid}}
	inputs := []dailySourceBriefInput{{Index: 0, SourceURL: "https://example.com/article", Body: "article body"}}

	_, err := translateDailySourceBodies(context.Background(), provider, inputs)
	if err == nil || !strings.Contains(err.Error(), "応答JSONを解析できません") {
		t.Fatalf("translateDailySourceBodies() error = %v, want existing parse error", err)
	}
	if provider.requests.Load() != 2 {
		t.Fatalf("generated-output-invalid must be bounded to two provider calls: requests=%d, want 2", provider.requests.Load())
	}
}

func TestDailyLLMStagesRetryGeneratedOutputInvalidOnce(t *testing.T) {
	tests := []struct {
		name  string
		valid string
		run   func(*scriptedDailyOutputProvider) error
	}{
		{
			name:  "term extraction",
			valid: `{"items":[{"index":0,"terms":[]}]}`,
			run: func(provider *scriptedDailyOutputProvider) error {
				_, err := extractDailyTerms(context.Background(), provider, []dailySourceBriefInput{{Index: 0, Body: "本文"}})
				return err
			},
		},
		{
			name:  "term resolution",
			valid: `{"items":[{"item_index":0,"term_index":0,"explanation":"日本語の用語補足です。"}]}`,
			run: func(provider *scriptedDailyOutputProvider) error {
				_, err := resolveDailyTerms(context.Background(), provider, []dailyTermLookupEvidence{{ItemIndex: 0, TermIndex: 0, Term: "term", SourceURL: "https://example.com/reference", Body: "本文"}})
				return err
			},
		},
		{
			name:  "brief",
			valid: `{"items":[{"index":0,"summary":"日本語の要約です。","perspective":"Shiroの見解: 日本語の見解です。"}]}`,
			run: func(provider *scriptedDailyOutputProvider) error {
				_, err := createDailyBriefs(context.Background(), provider, []dailyBriefLLMInput{{Index: 0, Title: "記事", SourceURL: "https://example.com/article", Body: "本文", TranslatedBody: "翻訳"}})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &scriptedDailyOutputProvider{responses: []string{"not-json", tt.valid}}
			if err := tt.run(provider); err != nil {
				t.Fatalf("stage error = %v", err)
			}
			if provider.requests.Load() != 2 {
				t.Fatalf("generated-output-invalid should consume one regeneration: requests=%d, want 2", provider.requests.Load())
			}
		})
	}
}

func TestGenerateDailyBriefLLMRetriesRetryableBoundaryFailureOnce(t *testing.T) {
	provider := &retryableDailyProvider{}
	response, err := generateDailyBriefLLM(context.Background(), provider, "translate_article", "工程: 原文翻訳")
	if err != nil {
		t.Fatalf("generateDailyBriefLLM() error = %v", err)
	}
	if !strings.Contains(response.Content, "再試行後") || provider.requests.Load() != 2 {
		t.Fatalf("bounded retry was not used: requests=%d response=%q", provider.requests.Load(), response.Content)
	}
}

func TestGenerateDailyBriefLLMWaitsAndRetriesCapacityCancellationAtSameStage(t *testing.T) {
	provider := &lowerLayerCanceledDailyProvider{}
	response, err := generateDailyBriefLLM(context.Background(), provider, "translate_article", "工程: 原文翻訳")
	if err != nil {
		t.Fatalf("generateDailyBriefLLM() error = %v", err)
	}
	if !strings.Contains(response.Content, "再開後") || provider.requests.Load() != 2 {
		t.Fatalf("current stage was not resumed: requests=%d response=%q", provider.requests.Load(), response.Content)
	}
}

type lowerLayerCanceledDailyProvider struct{ requests atomic.Int32 }

func (p *lowerLayerCanceledDailyProvider) Name() string { return "cancel-daily-worker" }
func (p *lowerLayerCanceledDailyProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.requests.Add(1) == 1 {
		return llm.GenerateResponse{}, context.Canceled
	}
	return dailyEnrichmentTestResponse(req.Messages[len(req.Messages)-1].Content, "再開後")
}

func TestDailyForegroundPriorityRetriesLowerLayerCancellation(t *testing.T) {
	provider := &lowerLayerCanceledDailyProvider{}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	job := orch.beginDailyEnrichmentJob(time.Now())
	if job == nil {
		t.Fatal("daily enrichment job was not created")
	}
	defer orch.finishDailyEnrichmentJob(job)
	events := []string{}
	got, err := orch.buildDailySourceBriefBatchWithForegroundPriority(job, provider, &dailySourceBriefResearchStub{
		events:     &events,
		documents:  map[string]DailySourceDocument{"https://example.com/article": {URL: "https://example.com/article", Text: "article body"}},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}, []NewsSeed{{Title: "article", URL: "https://example.com/article"}})
	if err != nil {
		t.Fatalf("buildDailySourceBriefBatchWithForegroundPriority() error = %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].TranslatedBody, "再開後") {
		t.Fatalf("same item was not resumed: %#v", got)
	}
}

func TestNewDailyEnrichmentSupersedesOldJobAndPreventsStalePublication(t *testing.T) {
	oldURL := "https://example.com/old-article"
	newURL := "https://example.com/new-article"
	withDailySeedCache(t, &DailySeedCache{
		Date: "2026-07-23", FetchedAt: time.Date(2026, 7, 23, 4, 0, 0, 0, jst), EnrichmentStatus: "pending",
		NewsSeedItems: []NewsSeed{{Title: "old-article", URL: oldURL}},
	})
	provider := &supersededDailyEnrichmentProvider{
		oldStarted:  make(chan struct{}),
		oldCanceled: make(chan struct{}),
		releaseOld:  make(chan struct{}),
		newStarted:  make(chan struct{}),
		newCanceled: make(chan struct{}),
		allowNew:    make(chan struct{}),
	}
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			oldURL: {URL: oldURL, Text: "old article body"},
			newURL: {URL: newURL, Text: "new article body"},
		},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}
	orch := NewIdleChatOrchestrator(nil, nil, nil, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{"worker": provider})
	orch.SetDailySourceBriefResearch(research)

	oldDone := make(chan struct{})
	go func() {
		defer close(oldDone)
		orch.enrichCurrentDailySeeds()
	}()
	select {
	case <-provider.oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old daily enrichment did not start")
	}

	cacheMu.Lock()
	dailyCache = &DailySeedCache{
		Date: "2026-07-23", FetchedAt: time.Date(2026, 7, 23, 4, 1, 0, 0, jst), EnrichmentStatus: "pending",
		NewsSeedItems: []NewsSeed{{Title: "new-article", URL: newURL}},
	}
	cacheMu.Unlock()

	newDone := make(chan struct{})
	go func() {
		defer close(newDone)
		orch.enrichCurrentDailySeeds()
	}()
	select {
	case <-provider.oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("new daily cache did not cancel the old job")
	}
	close(provider.releaseOld)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("new daily enrichment started before joining the old job")
	}
	select {
	case <-provider.newStarted:
	case <-time.After(time.Second):
		t.Fatal("new daily enrichment did not start after joining the old job")
	}

	// If the old job still owned a shared batch cancel slot, this interrupt
	// would leave the new request running. The new job must own cancellation.
	orch.Interrupt("superseded-job-test")
	select {
	case <-provider.newCanceled:
	case <-time.After(time.Second):
		t.Fatal("interrupt did not cancel the current daily enrichment request")
	}
	close(provider.allowNew)
	select {
	case <-oldDone:
	case <-time.After(time.Second):
		t.Fatal("old daily enrichment did not join")
	}
	select {
	case <-newDone:
	case <-time.After(time.Second):
		t.Fatal("new daily enrichment did not finish")
	}

	got := getDailyCache()
	if got.FetchedAt.Minute() != 1 {
		t.Fatalf("stale job replaced the current daily cache: %+v", got)
	}
	if got.EnrichmentStatus != "ready" || !strings.Contains(got.NewsSeedItems[0].TranslatedBody, "新ジョブ") {
		t.Fatalf("new job did not publish its own result: %+v", got)
	}
	if strings.Contains(got.NewsSeedItems[0].TranslatedBody, "旧ジョブ") {
		t.Fatalf("old job published into the new cache: %+v", got)
	}
}
