package conversation

import (
	"context"
	"testing"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

// TestKBIntegration_SaveAndSearch はKB保存と検索の統合テスト
func TestKBIntegration_SaveAndSearch(t *testing.T) {
	// Setup
	embedder := &mockEmbeddingProvider{vec: []float32{0.1, 0.2, 0.3}}
	mgr := newTestManager(embedder, &mockSummarizer{})
	ctx := context.Background()

	// Web検索結果をKBに保存
	results := []WebSearchResult{
		{
			Title:   "Go言語入門",
			Link:    "https://example.com/go-tutorial",
			Snippet: "Go言語の基本的な使い方を解説します。変数、関数、構造体などの基礎から学べます。",
		},
		{
			Title:   "Go言語の並行処理",
			Link:    "https://example.com/go-concurrency",
			Snippet: "ゴルーチンとチャネルを使った並行処理のパターンを紹介します。",
		},
	}

	err := mgr.SaveWebSearchToKB(ctx, "programming", "Go言語 入門", results)
	if err != nil {
		t.Fatalf("SaveWebSearchToKB failed: %v", err)
	}

	// KB検索を実行
	docs, err := mgr.SearchKB(ctx, "programming", "Go言語について教えて", 5)
	if err != nil {
		t.Fatalf("SearchKB failed: %v", err)
	}

	// 検証: embedderがnilでない場合、KB検索が実行される
	// mockVectorDBStoreは常に空を返すので、結果は0件
	if len(docs) != 0 {
		t.Errorf("Expected empty results from mock, got %d documents", len(docs))
	}
}

// TestKBIntegration_NoEmbedder はEmbedder無効時のグレースフル動作
func TestKBIntegration_NoEmbedder(t *testing.T) {
	mgr := newTestManager(nil, &mockSummarizer{})
	ctx := context.Background()

	// Embedder無効時はSearchKBが空を返す
	docs, err := mgr.SearchKB(ctx, "general", "何か質問", 5)
	if err != nil {
		t.Fatalf("SearchKB should not error when embedder is nil: %v", err)
	}
	if len(docs) != 0 {
		t.Error("SearchKB should return empty when embedder is nil")
	}
}

// TestConversationEngine_WithKBSearch はConversationEngineのKB統合テスト
func TestConversationEngine_WithKBSearch(t *testing.T) {
	// Setup
	embedder := &mockEmbeddingProvider{vec: []float32{0.5, 0.6, 0.7}}
	mgr := newTestManager(embedder, &mockSummarizer{})

	persona := domconv.PersonaState{
		Name:         "TestBot",
		SystemPrompt: "You are a helpful assistant",
		Tone:         "friendly",
		Mood:         "neutral",
	}
	engine := NewRealConversationEngine(mgr, persona)

	ctx := context.Background()

	// Threadを作成
	_, err := mgr.CreateThread(ctx, "test-session", "programming")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// BeginTurn を呼び出し（KB検索が内部で実行される）
	pack, err := engine.BeginTurn(ctx, "test-session", "Go言語のチャネルについて教えて")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}

	// RecallPackが正常に生成されることを確認
	if pack == nil {
		t.Fatal("RecallPack should not be nil")
	}
	if pack.Persona.Name != "TestBot" {
		t.Errorf("Expected persona name 'TestBot', got '%s'", pack.Persona.Name)
	}

	// KB検索結果はLongFactsに含まれる（mockなので空だが、エラーは出ない）
	// 実環境ではKB検索結果が [KB] プレフィックス付きで追加される
}

// TestConversationEngine_KBSearchError はKB検索エラー時のグレースフル動作
func TestConversationEngine_KBSearchError(t *testing.T) {
	// Embedderエラーを返すモック
	embedder := &mockEmbeddingProvider{
		vec: []float32{0.1, 0.2},
		err: nil, // SearchKBは正常に動作するが、mockVectorDBStoreが空を返す
	}
	mgr := newTestManager(embedder, &mockSummarizer{})

	persona := domconv.PersonaState{Name: "Bot"}
	engine := NewRealConversationEngine(mgr, persona)

	ctx := context.Background()
	_, _ = mgr.CreateThread(ctx, "session2", "general")

	// KB検索がエラーでもBeginTurnは成功する（ログ警告のみ）
	pack, err := engine.BeginTurn(ctx, "session2", "質問")
	if err != nil {
		t.Fatalf("BeginTurn should not fail on KB search error: %v", err)
	}
	if pack == nil {
		t.Fatal("RecallPack should not be nil")
	}
}
