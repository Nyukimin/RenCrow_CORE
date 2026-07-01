package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/mcp"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func main() {
	// コマンドライン引数でメッセージを受け取る
	if len(os.Args) < 2 {
		fmt.Println("Usage: test-chat <message>")
		fmt.Println("Example: test-chat 'Go言語について教えて'")
		os.Exit(1)
	}
	userMessage := os.Args[1]

	// 設定読み込み
	cfg, err := config.LoadConfig("./config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 依存関係構築
	ctx := context.Background()

	// LLMプロバイダー
	ollamaProvider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)

	// ToolRunner（Google Search API設定込み）
	toolConfig := tools.ToolRunnerConfig{
		GoogleAPIKey:         os.Getenv("GOOGLE_API_KEY_CHAT"),
		GoogleSearchEngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID_CHAT"),
	}
	toolRunner := tools.NewToolRunner(toolConfig)

	// MCPClient
	mcpClient := mcp.NewMCPClient()

	// セッションリポジトリ
	sessionRepo := session.NewJSONSessionRepository(cfg.Session.StorageDir)

	// プロンプト読み込み
	prompts := config.LoadPrompts(cfg.PromptsDir, cfg.WorkspaceDir)

	// ルーティング
	ruleDictionary := routing.NewRuleDictionary()
	classifier := routing.NewLLMClassifier(ollamaProvider, prompts.Classifier)

	// Mio（Chat Agent）
	mio := agent.NewMioAgent(ollamaProvider, classifier, ruleDictionary, toolRunner, mcpClient, nil) // conversationEngine=nil（テスト環境）

	// WorkerExecutionService
	workerService := service.NewWorkerExecutionService(cfg.Worker)

	// MessageOrchestrator
	orch := orchestrator.NewMessageOrchestrator(
		sessionRepo,
		mio,
		nil, // Shiro（不要）
		nil, // Coder1（不要）
		nil, // Coder2（不要）
		nil, // Coder3（不要）
		nil, // Coder4（不要）
		workerService,
	)

	// メッセージ処理
	req := orchestrator.ProcessMessageRequest{
		SessionID:   "test-session-001",
		Channel:     "cli",
		ChatID:      "test-user",
		UserMessage: userMessage,
	}

	fmt.Printf("📨 Input: %s\n", userMessage)
	fmt.Println("⏳ Processing...")

	resp, err := orch.ProcessMessage(ctx, req)
	if err != nil {
		log.Fatalf("❌ Error: %v", err)
	}

	fmt.Printf("\n✅ Response:\n%s\n", resp.Response)
}
