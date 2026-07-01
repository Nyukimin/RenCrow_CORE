//go:build integration

package conversation

import (
	"context"
	"net/http"
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

const (
	testRedisURL    = "redis://localhost:6379"
	testVectorDBURL = "localhost:6334"
	testCollection  = "picoclaw_integration_test"
)

// skipIfUnavailable は接続確認し、未起動なら t.Skip()
func skipIfRedisUnavailable(t *testing.T) {
	t.Helper()
	store, err := NewRedisStore(testRedisURL)
	if err != nil {
		t.Skipf("Redis unavailable (%v) — run: docker compose -f docker-compose.infra.yml up -d", err)
	}
	store.Close()
}

func skipIfQdrantUnavailable(t *testing.T) {
	t.Helper()
	resp, err := http.Get("http://localhost:6333/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Skipf("Qdrant unavailable — run: docker compose -f docker-compose.infra.yml up -d")
	}
	resp.Body.Close()
}

// --- RedisStore 統合テスト ---

func TestRedisStore_Integration_SessionAndThread(t *testing.T) {
	skipIfRedisUnavailable(t)

	store, err := NewRedisStore(testRedisURL)
	if err != nil {
		t.Fatalf("NewRedisStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	sessionID := "integration-sess-" + time.Now().Format("150405")

	// Session保存・取得
	sess := domconv.NewSessionConversation(sessionID, "test-user")
	if err := store.SaveSession(ctx, sess); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}
	got, err := store.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got.ID != sessionID {
		t.Errorf("Expected session ID %s, got %s", sessionID, got.ID)
	}

	// Thread保存・取得
	thread := domconv.NewThread(sessionID, "integration")
	thread.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "統合テストのメッセージ", nil))
	if err := store.SaveThread(ctx, thread); err != nil {
		t.Fatalf("SaveThread failed: %v", err)
	}
	gotThread, err := store.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if len(gotThread.Turns) != 1 {
		t.Errorf("Expected 1 turn, got %d", len(gotThread.Turns))
	}

	// クリーンアップ
	store.DeleteThread(ctx, thread.ID)
	store.DeleteSession(ctx, sessionID)
}

// --- VectorDBStore 統合テスト ---

func TestVectorDBStore_Integration_SaveAndSearch(t *testing.T) {
	skipIfQdrantUnavailable(t)

	store, err := NewVectorDBStore(testVectorDBURL, testCollection)
	if err != nil {
		t.Fatalf("NewVectorDBStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now()

	// Thread要約を保存（embedding付き）
	summary := &domconv.ThreadSummary{
		ThreadID:  now.UnixNano(),
		SessionID: "integration-sess",
		Domain:    "test",
		Summary:   "統合テスト用の会話要約",
		Keywords:  []string{"統合", "テスト", "Qdrant"},
		Embedding: make([]float32, 768), // ゼロベクトル（テスト用）
		StartTime: now.Add(-5 * time.Minute),
		EndTime:   now,
		IsNovel:   true,
	}
	// ゼロベクトル以外にする（検索で見つかるように）
	for i := range summary.Embedding {
		summary.Embedding[i] = float32(i%10) * 0.01
	}

	if err := store.SaveThreadSummary(ctx, summary); err != nil {
		t.Fatalf("SaveThreadSummary failed: %v", err)
	}

	// 類似度検索（同じembeddingで検索）
	results, err := store.SearchSimilar(ctx, summary.Embedding, 3)
	if err != nil {
		t.Fatalf("SearchSimilar failed: %v", err)
	}
	if len(results) == 0 {
		t.Error("Expected at least 1 search result")
	}
	// スコアが設定されている
	if results[0].Score == 0 {
		t.Error("Expected non-zero similarity score")
	}
	t.Logf("SearchSimilar: %d results, top score: %.4f", len(results), results[0].Score)

	// IsNovelQuery（同じembeddingは類似度高い → 新規でない）
	isNovel, score, err := store.IsNovelQuery(ctx, summary.Embedding, 0.85)
	if err != nil {
		t.Fatalf("IsNovelQuery failed: %v", err)
	}
	t.Logf("IsNovelQuery: isNovel=%v, score=%.4f", isNovel, score)
	// 同一embeddingなので score は SearchSimilar の実スコア（≒1.0）と一致するはず
	if len(results) > 0 && score != results[0].Score {
		t.Errorf("IsNovelQuery score %.4f should match SearchSimilar top score %.4f", score, results[0].Score)
	}
	// 高類似度（≥0.85）なので新規でない
	if isNovel {
		t.Errorf("Should not be novel when top score=%.4f >= threshold=0.85", score)
	}
}

// --- RealConversationManager 統合テスト ---

func TestRealConversationManager_Integration_StoreAndRecall(t *testing.T) {
	skipIfRedisUnavailable(t)

	// VectorDBはオプション（未起動でもテスト続行）
	vdbAvailable := true
	if resp, err := http.Get("http://localhost:6333/healthz"); err != nil || resp.StatusCode != 200 {
		vdbAvailable = false
		t.Log("Qdrant unavailable, skipping VectorDB recall test")
	} else {
		resp.Body.Close()
	}

	var mgr *RealConversationManager
	var err error
	if vdbAvailable {
		mgr, err = NewRealConversationManager(testRedisURL, "", testVectorDBURL)
	} else {
		mgr, err = NewRealConversationManager(testRedisURL, "", "localhost:19999") // ダミー
	}
	_ = err // 接続エラーは許容（Qdrantが起動していれば接続成功）

	// Redisのみのシンプルなテスト
	redisStore, err := NewRedisStore(testRedisURL)
	if err != nil {
		t.Fatalf("RedisStore failed: %v", err)
	}
	defer redisStore.Close()

	simpleMgr := &RealConversationManager{
		redisStore:    redisStore,
		duckdbStore:   &mockDuckDBStore{},
		vectordbStore: &mockVectorDBStore{mockScore: 0.5},
		embedder:      nil,
		summarizer:    nil,
	}
	_ = mgr

	ctx := context.Background()
	sessionID := "integration-full-" + time.Now().Format("150405")

	// メッセージを保存
	msgs := []string{"こんにちは", "Go言語は素晴らしい", "ありがとう"}
	for _, text := range msgs {
		msg := domconv.NewMessage(domconv.SpeakerUser, text, nil)
		if err := simpleMgr.Store(ctx, sessionID, msg); err != nil {
			t.Fatalf("Store failed: %v", err)
		}
	}

	// Recallで取得
	recalled, err := simpleMgr.Recall(ctx, sessionID, "Go言語", 5)
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(recalled) == 0 {
		t.Error("Expected recalled messages, got none")
	}
	t.Logf("Recalled %d messages from Redis", len(recalled))

	// クリーンアップ
	thread, _ := simpleMgr.GetActiveThread(ctx, sessionID)
	if thread != nil {
		redisStore.DeleteThread(ctx, thread.ID)
	}
	redisStore.DeleteSession(ctx, sessionID)
}

// --- ConversationEngine E2E 統合テスト ---

func TestConversationEngine_Integration_E2E(t *testing.T) {
	skipIfRedisUnavailable(t)
	skipIfQdrantUnavailable(t)

	ctx := context.Background()
	sessionID := "e2e-engine-" + time.Now().Format("150405.000")

	// 1. RealConversationManager（Redis + DuckDB + VectorDB）
	redisStore, err := NewRedisStore(testRedisURL)
	if err != nil {
		t.Fatalf("RedisStore failed: %v", err)
	}
	defer redisStore.Close()

	vdbStore, err := NewVectorDBStore(testVectorDBURL, testCollection)
	if err != nil {
		t.Fatalf("VectorDBStore failed: %v", err)
	}
	defer vdbStore.Close()

	mgr := &RealConversationManager{
		redisStore:    redisStore,
		duckdbStore:   &mockDuckDBStore{},
		vectordbStore: vdbStore,
	}

	// 2. ConversationEngine を構築
	persona := domconv.NewMioPersona("")
	engine := NewRealConversationEngine(mgr, persona)

	// 3. BeginTurn（初回 — 記憶なし）
	pack, err := engine.BeginTurn(ctx, sessionID, "こんにちは")
	if err != nil {
		t.Fatalf("BeginTurn failed: %v", err)
	}
	if pack == nil {
		t.Fatal("RecallPack should not be nil")
	}
	t.Logf("BeginTurn(initial): ShortContext=%d, MidSummaries=%d, LongFacts=%d",
		len(pack.ShortContext), len(pack.MidSummaries), len(pack.LongFacts))

	// Persona がセットされていること
	if pack.Persona.Name == "" {
		t.Error("Persona.Name should be set")
	}
	t.Logf("Persona: %s", pack.Persona.Name)

	// 4. EndTurn（メッセージ保存）
	err = engine.EndTurn(ctx, sessionID, "こんにちは", "こんにちは！何かお手伝いしますか？")
	if err != nil {
		t.Fatalf("EndTurn failed: %v", err)
	}

	// 5. 2回目の BeginTurn（短期記憶にメッセージあり）
	pack2, err := engine.BeginTurn(ctx, sessionID, "Go言語について教えて")
	if err != nil {
		t.Fatalf("BeginTurn(2nd) failed: %v", err)
	}
	if len(pack2.ShortContext) == 0 {
		t.Error("Expected short context from previous turn")
	}
	t.Logf("BeginTurn(2nd): ShortContext=%d messages", len(pack2.ShortContext))

	// 6. EndTurn（2回目）
	err = engine.EndTurn(ctx, sessionID, "Go言語について教えて", "Go言語は高速なプログラミング言語です。")
	if err != nil {
		t.Fatalf("EndTurn(2nd) failed: %v", err)
	}

	// 7. GetStatus
	status, err := engine.GetStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetStatus failed: %v", err)
	}
	if status.SessionID != sessionID {
		t.Errorf("Expected sessionID=%s, got %s", sessionID, status.SessionID)
	}
	if status.TurnCount < 2 {
		t.Errorf("Expected at least 2 turns, got %d", status.TurnCount)
	}
	t.Logf("Status: session=%s, thread=%d, turns=%d, domain=%s",
		status.SessionID, status.ThreadID, status.TurnCount, status.ThreadDomain)

	// 8. FlushCurrentThread → DuckDB/VectorDB 保存
	err = engine.FlushCurrentThread(ctx, sessionID)
	if err != nil {
		t.Fatalf("FlushCurrentThread failed: %v", err)
	}

	// Flush 後の GetStatus で新しい Thread が作成されていること
	statusAfter, err := engine.GetStatus(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetStatus after flush failed: %v", err)
	}
	if statusAfter.ThreadID == status.ThreadID {
		t.Error("ThreadID should be different after flush")
	}
	if statusAfter.TurnCount != 0 {
		t.Errorf("New thread should have 0 turns, got %d", statusAfter.TurnCount)
	}
	t.Logf("After flush: new thread=%d, turns=%d", statusAfter.ThreadID, statusAfter.TurnCount)

	// 9. ResetSession
	err = engine.ResetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ResetSession failed: %v", err)
	}

	// クリーンアップ
	thread, _ := mgr.GetActiveThread(ctx, sessionID)
	if thread != nil {
		redisStore.DeleteThread(ctx, thread.ID)
	}
	redisStore.DeleteSession(ctx, sessionID)
	t.Log("E2E test completed successfully")
}
