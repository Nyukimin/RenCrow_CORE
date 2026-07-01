package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/mcp"
	infraRouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func main() {
	fmt.Println("=== RenCrow Mio Web Search Test ===")

	// 1. ToolRunner初期化（Web検索含む）
	cfg := tools.ToolRunnerConfig{
		GoogleAPIKey:         os.Getenv("GOOGLE_API_KEY_CHAT"),
		GoogleSearchEngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID_CHAT"),
	}

	if cfg.GoogleAPIKey == "" || cfg.GoogleSearchEngineID == "" {
		fmt.Println("Error: Google Search API not configured")
		fmt.Println("Please set GOOGLE_API_KEY_CHAT and GOOGLE_SEARCH_ENGINE_ID_CHAT")
		os.Exit(1)
	}

	toolRunner := tools.NewToolRunner(cfg)
	availableTools, _ := toolRunner.List(context.Background())
	fmt.Printf("Available tools: %v\n\n", availableTools)

	// 2. LLM Provider（Ollama）
	ollamaBaseURL := os.Getenv("OLLAMA_BASE_URL")
	if ollamaBaseURL == "" {
		ollamaBaseURL = "http://localhost:11434"
	}
	ollamaModel := os.Getenv("OLLAMA_MODEL")
	if ollamaModel == "" {
		ollamaModel = "Chat"
	}
	ollamaProvider := ollama.NewOllamaProvider(ollamaBaseURL, ollamaModel)

	// 3. MioAgent作成
	prompts := config.LoadPrompts("", "")
	classifier := infraRouting.NewLLMClassifier(ollamaProvider, prompts.Classifier)
	ruleDictionary := infraRouting.NewRuleDictionary()
	mcpClient := mcp.NewMCPClient()

	mioAgent := agent.NewMioAgent(
		ollamaProvider,
		classifier,
		ruleDictionary,
		toolRunner,
		mcpClient,
		nil, // conversationEngine=nil（テスト環境）
	)

	// 4. テストメッセージ
	testMessages := []string{
		"Go言語について教えて",
		"こんにちは",
		"今日のニュースを調べて",
	}

	for i, msg := range testMessages {
		fmt.Printf("--- Test %d: %s ---\n", i+1, msg)

		jobID := task.NewJobID()
		testTask := task.NewTask(jobID, msg, "test", "test_user")

		// ルーティング決定
		decision, err := mioAgent.DecideAction(context.Background(), testTask)
		if err != nil {
			log.Printf("DecideAction failed: %v\n", err)
			continue
		}
		fmt.Printf("Route: %s (confidence: %.2f, reason: %s)\n",
			decision.Route, decision.Confidence, decision.Reason)

		// CHAT実行（Web検索が自動的に実行されるはず）
		if decision.Route == routing.RouteCHAT {
			response, err := mioAgent.Chat(context.Background(), testTask)
			if err != nil {
				log.Printf("Chat failed: %v\n", err)
			} else {
				fmt.Printf("Response: %s\n", response)
			}
		}

		fmt.Println()
	}
}
