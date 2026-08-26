package idlechat

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type dailySourceBriefResearchStub struct {
	events        *[]string
	documents     map[string]DailySourceDocument
	readErrors    map[string]error
	searchResults map[string][]DailyTermSearchResult
	searchErrors  map[string]error
}

func (s *dailySourceBriefResearchStub) ReadURL(_ context.Context, rawURL string) (DailySourceDocument, error) {
	*s.events = append(*s.events, "本文取得:"+rawURL)
	if err := s.readErrors[rawURL]; err != nil {
		return DailySourceDocument{}, err
	}
	return s.documents[rawURL], nil
}

func (s *dailySourceBriefResearchStub) SearchTerm(_ context.Context, term, query string) ([]DailyTermSearchResult, error) {
	*s.events = append(*s.events, "用語検索:"+term+":"+query)
	if err := s.searchErrors[term]; err != nil {
		return nil, err
	}
	return append([]DailyTermSearchResult(nil), s.searchResults[term]...), nil
}

type orderedDailyBriefProvider struct {
	events   *[]string
	requests []llm.GenerateRequest
}

func (p *orderedDailyBriefProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	prompt := req.Messages[len(req.Messages)-1].Content
	switch {
	case strings.Contains(prompt, "工程: 記事統合分析"):
		*p.events = append(*p.events, "LLM:記事統合分析")
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"新しいRAG検索支援機能を提供します。LLMへの入力に検索資料を追加し、回答を根拠づけます。","terms":[{"term":"LLM","explanation":"文章を生成・理解する大規模言語モデルです。","needs_lookup":false},{"term":"RAG","explanation":"本文だけでは意味を特定できません。","needs_lookup":true,"lookup_query":"RAG 公式 定義"}],"summary":"本文では、RAGを使った新しい検索支援機能が発表されています。","perspective":"Shiroの見解: 評価条件と情報源の品質を確認してから導入判断をするのがよいです。"}]}`}, nil
	case strings.Contains(prompt, "工程: 原文翻訳"):
		*p.events = append(*p.events, "LLM:原文翻訳")
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"translated_body":"新しいRAG検索支援機能を提供します。LLMへの入力に検索資料を追加し、回答を根拠づけます。"}]}`}, nil
	case strings.Contains(prompt, "工程: 用語抽出"):
		*p.events = append(*p.events, "LLM:用語抽出")
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"terms":[{"term":"LLM","explanation":"文章を生成・理解する大規模言語モデルです。","needs_lookup":false},{"term":"RAG","explanation":"本文だけでは意味を特定できません。","needs_lookup":true,"lookup_query":"RAG 公式 定義"}]}]}`}, nil
	case strings.Contains(prompt, "工程: 不明語補足"):
		*p.events = append(*p.events, "LLM:不明語補足")
		return llm.GenerateResponse{Content: `{"items":[{"item_index":0,"term_index":1,"explanation":"検索拡張生成の略で、検索した外部情報を生成モデルへの入力に加える手法です。"}]}`}, nil
	case strings.Contains(prompt, "工程: サマリと見解"):
		*p.events = append(*p.events, "LLM:サマリと見解")
		return llm.GenerateResponse{Content: `{"items":[{"index":0,"summary":"本文では、RAGを使った新しい検索支援機能が発表されています。","perspective":"Shiroの見解: 評価条件と情報源の品質を確認してから導入判断をするのがよいです。"}]}`}, nil
	default:
		return llm.GenerateResponse{}, errors.New("想定外の工程です")
	}
}

func (p *orderedDailyBriefProvider) Name() string { return "collection-test-worker" }

type failingDailyBriefStageProvider struct {
	failStage string
	orderedDailyBriefProvider
}

func (p *failingDailyBriefStageProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	prompt := req.Messages[len(req.Messages)-1].Content
	if strings.Contains(prompt, p.failStage) {
		return llm.GenerateResponse{}, errors.New("backend detail must stay in logs")
	}
	return p.orderedDailyBriefProvider.Generate(ctx, req)
}

func TestBuildDailySourceBriefUsesOneIntegratedAnalysisAndSearchesOnlyUnknownTerms(t *testing.T) {
	articleURL := "https://example.com/articles/rag"
	definitionURL := "https://example.org/reference/rag"
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL:    {URL: articleURL, Text: "The source announces a new RAG retrieval support feature. It adds search material to LLM input and grounds responses."},
			definitionURL: {URL: definitionURL, Text: "RAGは、検索した外部情報を生成モデルへの入力に加える手法です。"},
		},
		readErrors: map[string]error{},
		searchResults: map[string][]DailyTermSearchResult{
			"RAG": {{Title: "RAGの解説", URL: definitionURL}},
		},
		searchErrors: map[string]error{},
	}
	provider := &orderedDailyBriefProvider{events: &events}

	got, err := buildDailySourceBriefBatch(context.Background(), provider, research, []NewsSeed{{
		Title: "RAG検索支援機能を発表", URL: articleURL, Source: "公式ニュース", SourceType: "rss",
	}})
	if err != nil {
		t.Fatalf("buildDailySourceBriefBatch: %v", err)
	}
	wantEvents := []string{
		"本文取得:" + articleURL,
		"LLM:記事統合分析",
		"用語検索:RAG:RAG 公式 定義",
		"本文取得:" + definitionURL,
		"LLM:不明語補足",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("工程順序 = %#v, want %#v", events, wantEvents)
	}
	if len(got) != 1 || len(got[0].TermNotes) != 2 {
		t.Fatalf("用語補足 = %+v", got)
	}
	if got[0].TermNotes[0].Term != "LLM" || got[0].TermNotes[0].SourceKind != "article_context" {
		t.Fatalf("本文で意味が明確な用語 = %+v", got[0].TermNotes[0])
	}
	if got[0].TermNotes[1].Term != "RAG" || got[0].TermNotes[1].SourceURL != definitionURL || got[0].TermNotes[1].Status != "confirmed" {
		t.Fatalf("検索で確認した用語 = %+v", got[0].TermNotes[1])
	}
	if got[0].TranslatedBody == "" || got[0].Summary == "" || got[0].Perspective == "" || got[0].SourceReadStatus != "ready" || got[0].ProcessingStatus != "ready" || got[0].ProcessingError != "" {
		t.Fatalf("ブリーフ = %+v", got[0])
	}
	if len(provider.requests) != 2 {
		t.Fatalf("LLM呼び出し回数 = %d", len(provider.requests))
	}
	resolutionPrompt := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	if !strings.Contains(resolutionPrompt, "RAGは、検索した外部情報") {
		t.Fatalf("不明語補足は検索先本文を根拠にする必要があります: %s", resolutionPrompt)
	}
}

func TestBuildDailySourceBriefDoesNotGuessWhenArticleBodyCannotBeRead(t *testing.T) {
	articleURL := "https://example.com/unavailable"
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events:        &events,
		documents:     map[string]DailySourceDocument{},
		readErrors:    map[string]error{articleURL: errors.New("取得失敗")},
		searchResults: map[string][]DailyTermSearchResult{},
		searchErrors:  map[string]error{},
	}
	provider := &orderedDailyBriefProvider{events: &events}

	got, err := buildDailySourceBriefBatch(context.Background(), provider, research, []NewsSeed{{
		Title: "本文未取得の記事", URL: articleURL, Summary: "フィードの説明文", SourceType: "rss",
	}})
	if err != nil {
		t.Fatalf("本文取得失敗は項目単位の明示状態にする: %v", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("本文未取得時にLLMへ見出しやフィード要約を渡してはならない: %d", len(provider.requests))
	}
	if got[0].SourceReadStatus != "unavailable" || got[0].ProcessingStatus != "source_unavailable" || !strings.Contains(got[0].Summary, "本文を取得できませんでした") {
		t.Fatalf("本文未取得の明示 = %+v", got[0])
	}
	if got[0].ProcessingError != "元URLから原文を取得できませんでした。" {
		t.Fatalf("本文取得失敗は安全な項目別理由を返す必要があります: %+v", got[0])
	}
	if !strings.Contains(got[0].TranslatedBody, "翻訳できませんでした") {
		t.Fatalf("本文未取得時は原文翻訳不能を明示する必要があります: %+v", got[0])
	}
	if len(got[0].TermNotes) != 1 || got[0].TermNotes[0].Status != "unavailable" {
		t.Fatalf("用語補足も未確認を明示する必要があります: %+v", got[0].TermNotes)
	}
}

func TestBuildDailySourceBriefPreservesSourceReadSuccessWhenIntegratedAnalysisFails(t *testing.T) {
	articleURL := "https://example.com/articles/translation-failure"
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL: {URL: articleURL, Text: "The source body was fetched successfully."},
		},
		readErrors: map[string]error{}, searchResults: map[string][]DailyTermSearchResult{}, searchErrors: map[string]error{},
	}
	provider := &failingDailyBriefStageProvider{
		failStage:                 "工程: 記事統合分析",
		orderedDailyBriefProvider: orderedDailyBriefProvider{events: &events},
	}

	got, err := buildDailySourceBriefBatch(context.Background(), provider, research, []NewsSeed{{Title: "翻訳失敗", URL: articleURL}})
	if err == nil {
		t.Fatal("記事統合分析失敗をbatch errorとして返す必要があります")
	}
	if len(got) != 1 {
		t.Fatalf("失敗時も項目別の途中状態を返す必要があります: %+v", got)
	}
	item := got[0]
	if item.SourceReadStatus != "ready" || item.SourceReadURL != articleURL {
		t.Fatalf("原文取得成功を翻訳失敗で失ってはいけません: %+v", item)
	}
	if item.ProcessingStatus != "translation_failed" || item.ProcessingError != "原文翻訳のLLM応答を完了できませんでした。" {
		t.Fatalf("翻訳工程の失敗を安全に明示する必要があります: %+v", item)
	}
	if item.TranslatedBody != "原文の取得は完了しましたが、翻訳に失敗しました。" {
		t.Fatalf("原文取得失敗と翻訳失敗を区別する必要があります: %+v", item)
	}
	if strings.Contains(item.ProcessingError, "backend detail") {
		t.Fatalf("backend errorをViewer向け項目へ漏らしてはいけません: %+v", item)
	}
}

func TestBuildDailySourceBriefMarksUnknownTermUnresolvedWhenSearchFails(t *testing.T) {
	articleURL := "https://example.com/articles/rag"
	events := []string{}
	research := &dailySourceBriefResearchStub{
		events: &events,
		documents: map[string]DailySourceDocument{
			articleURL: {URL: articleURL, Text: "新しいRAG検索支援機能を提供します。"},
		},
		readErrors:    map[string]error{},
		searchResults: map[string][]DailyTermSearchResult{},
		searchErrors:  map[string]error{"RAG": errors.New("検索失敗")},
	}
	provider := &orderedDailyBriefProvider{events: &events}

	got, err := buildDailySourceBriefBatch(context.Background(), provider, research, []NewsSeed{{Title: "RAG", URL: articleURL}})
	if err != nil {
		t.Fatalf("用語検索失敗は項目単位の明示状態にする: %v", err)
	}
	note := got[0].TermNotes[1]
	if note.Status != "unresolved" || !strings.Contains(note.Explanation, "意味を確認できませんでした") {
		t.Fatalf("不明語をごまかさず未解決にする必要があります: %+v", note)
	}
}

func TestParseDailyBriefRejectsNonJapaneseBody(t *testing.T) {
	_, err := parseDailyBriefResponse(`{"items":[{"index":0,"summary":"This is a summary.","perspective":"This is my view."}]}`, 1)
	if err == nil {
		t.Fatal("日本語を含まない本文は拒否する必要があります")
	}
}

func TestParseDailyTranslationRejectsNonJapaneseBody(t *testing.T) {
	_, err := parseDailyTranslationResponse(`{"items":[{"index":0,"translated_body":"This is a translation."}]}`, 1)
	if err == nil {
		t.Fatal("日本語を含まない原文翻訳は拒否する必要があります")
	}
}
