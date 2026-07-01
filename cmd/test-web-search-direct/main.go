package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func main() {
	fmt.Println("=== Direct Web Search Test (Chat) ===")

	cfg := tools.ToolRunnerConfig{
		GoogleAPIKey:       os.Getenv("GOOGLE_API_KEY_CHAT"),
		GoogleSearchEngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID_CHAT"),
	}

	if cfg.GoogleAPIKey == "" || cfg.GoogleSearchEngineID == "" {
		fmt.Println("Error: Google Search API not configured")
		fmt.Println("Please set GOOGLE_API_KEY_CHAT and GOOGLE_SEARCH_ENGINE_ID_CHAT environment variables")
		os.Exit(1)
	}

	fmt.Printf("Using API Key: %s... (Engine ID: %s)\n\n", cfg.GoogleAPIKey[:20], cfg.GoogleSearchEngineID)
	toolRunner := tools.NewToolRunner(cfg)

	testQueries := []string{
		"golang programming language",
		"Go言語",
		"今日のニュース",
		"weather tokyo",
	}

	for i, query := range testQueries {
		fmt.Printf("--- Test %d: %s ---\n", i+1, query)

		args := map[string]interface{}{
			"query": query,
		}

		result, err := toolRunner.Execute(context.Background(), "web_search", args)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		} else {
			fmt.Printf("Result:\n%s\n", result)
		}
		fmt.Println()
	}
}
