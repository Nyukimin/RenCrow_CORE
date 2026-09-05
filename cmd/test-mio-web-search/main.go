package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	infraRouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

	// 2. LLM Provider (RenCrow_LLM logical alias)
	gatewayBaseURL := strings.TrimSpace(os.Getenv("RENCROW_LLM_URL"))
	if gatewayBaseURL == "" {
		gatewayBaseURL = "http://127.0.0.1:8090"
	}
	gatewayProvider := rencrowllm.NewGatewayProviderWithOptions(
		strings.TrimSpace(os.Getenv("RENCROW_LLM_API_KEY")),
		"mio",
		gatewayBaseURL,
		10*time.Minute,
	)

	// 3. MioAgent作成
	prompts := config.LoadPrompts("", "")
	classifier := infraRouting.NewLLMClassifier(gatewayProvider, prompts.Classifier)
	ruleDictionary := infraRouting.NewRuleDictionary()
	mcpClient := mcp.NewMCPClient()

	mioAgent := agent.NewMioAgent(
		gatewayProvider,
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

		address, err := conversation.NewChannelAddress("test", "test_user")
		if err != nil {
			log.Printf("ChannelAddress construction failed: %v\n", err)
			continue
		}
		input, err := conversation.NewTurnInput(modulecore.NewTaskID(), msg, address)
		if err != nil {
			log.Printf("TurnInput construction failed: %v\n", err)
			continue
		}
		input = input.WithSessionID(string(modulecore.NewSessionID()))

		// ルーティング決定
		decision, err := mioAgent.DecideAction(context.Background(), input)
		if err != nil {
			log.Printf("DecideAction failed: %v\n", err)
			continue
		}
		fmt.Printf("Route: %s (confidence: %.2f, reason: %s)\n",
			decision.Route, decision.Confidence, decision.Reason)

		// CHAT実行（Web検索が自動的に実行されるはず）
		if decision.Route == routing.RouteCHAT {
			response, err := mioAgent.Chat(context.Background(), input)
			if err != nil {
				log.Printf("Chat failed: %v\n", err)
			} else {
				fmt.Printf("Response: %s\n", response)
			}
		}

		fmt.Println()
	}
}
