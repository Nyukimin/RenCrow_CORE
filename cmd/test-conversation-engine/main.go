package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

func main() {
	fmt.Println("=== ConversationEngine v5.1 E2E Test ===")

	// 1. ConversationManager + Engine 初期化
	redisURL := envOrDefault("REDIS_URL", "redis://localhost:6379")
	duckdbPath := envOrDefault("DUCKDB_PATH", "/tmp/test_conversation_engine.duckdb")
	vectordbURL := envOrDefault("VECTORDB_URL", "localhost:6334")

	// テスト用DuckDB（既存のものと競合しないように別パス）
	defer os.Remove(duckdbPath)
	defer os.Remove(duckdbPath + ".wal")

	realMgr, err := conversationpersistence.NewRealConversationManager(redisURL, duckdbPath, vectordbURL)
	if err != nil {
		log.Fatalf("Failed to init ConversationManager: %v", err)
	}

	engine := conversationpersistence.NewRealConversationEngine(
		realMgr,
		conversation.DefaultMioPersona(),
	)

	ctx := context.Background()
	sessionID := "test-session-e2e-v51"

	// 2. 1回目のターン
	fmt.Println("\n--- Turn 1: BeginTurn ---")
	pack1, err := engine.BeginTurn(ctx, sessionID, "Go言語について教えて")
	if err != nil {
		log.Fatalf("BeginTurn 1 failed: %v", err)
	}
	fmt.Printf("RecallPack.HasContext: %v\n", pack1.HasContext())
	fmt.Printf("RecallPack.Persona.Name: %s\n", pack1.Persona.Name)
	fmt.Printf("RecallPack.ShortContext count: %d\n", len(pack1.ShortContext))
	fmt.Printf("RecallPack.MidSummaries count: %d\n", len(pack1.MidSummaries))
	fmt.Printf("RecallPack.LongFacts count: %d\n", len(pack1.LongFacts))

	// プロンプトメッセージ生成
	msgs1 := pack1.ToPromptMessages()
	fmt.Printf("PromptMessages count: %d\n", len(msgs1))
	for i, m := range msgs1 {
		content := m.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		fmt.Printf("  [%d] role=%s content=%q\n", i, m.Role, content)
	}

	fmt.Println("\n--- Turn 1: EndTurn ---")
	err = engine.EndTurn(ctx, sessionID, "Go言語について教えて", "Go言語はGoogleが開発したプログラミング言語です。シンプルで高速なのが特徴です。")
	if err != nil {
		log.Fatalf("EndTurn 1 failed: %v", err)
	}
	fmt.Println("EndTurn 1: OK (user + mio messages stored)")

	// 3. 2回目のターン（Recallで前回の会話が返るはず）
	fmt.Println("\n--- Turn 2: BeginTurn ---")
	pack2, err := engine.BeginTurn(ctx, sessionID, "さっき何の話してた？")
	if err != nil {
		log.Fatalf("BeginTurn 2 failed: %v", err)
	}
	fmt.Printf("RecallPack.HasContext: %v\n", pack2.HasContext())
	fmt.Printf("RecallPack.ShortContext count: %d\n", len(pack2.ShortContext))
	fmt.Printf("RecallPack.MidSummaries count: %d\n", len(pack2.MidSummaries))
	fmt.Printf("RecallPack.LongFacts count: %d\n", len(pack2.LongFacts))

	// ShortContext に前回の会話が入っているか
	if len(pack2.ShortContext) > 0 {
		fmt.Println("\n  ShortContext (recalled from previous turn):")
		for i, m := range pack2.ShortContext {
			content := m.Msg
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			fmt.Printf("    [%d] speaker=%s msg=%q\n", i, m.Speaker, content)
		}
	}

	// プロンプトメッセージ生成（2回目）
	msgs2 := pack2.ToPromptMessages()
	fmt.Printf("\nPromptMessages count: %d\n", len(msgs2))
	for i, m := range msgs2 {
		content := m.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		fmt.Printf("  [%d] role=%s content=%q\n", i, m.Role, content)
	}

	// 4. 判定
	fmt.Println("\n=== Result ===")
	if pack2.HasContext() && len(pack2.ShortContext) >= 2 {
		fmt.Println("PASS: 2回目のBeginTurnで前回の会話がRecallPackに含まれている")
	} else if pack2.HasContext() {
		fmt.Println("PARTIAL: RecallPackにコンテキストはあるが、ShortContextが期待より少ない")
	} else {
		fmt.Println("FAIL: 2回目のBeginTurnでRecallPackが空")
	}

	// クリーンアップ: テスト用Redisキーを削除
	fmt.Println("\nCleanup: test session data")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
