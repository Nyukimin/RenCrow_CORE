package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func main() {
	fmt.Println("=== Worker Search Test ===")

	cfg := tools.ToolRunnerConfig{
		GoogleAPIKey:       os.Getenv("GOOGLE_API_KEY_WORKER"),
		GoogleSearchEngineID: os.Getenv("GOOGLE_SEARCH_ENGINE_ID_WORKER"),
	}

	if cfg.GoogleAPIKey == "" || cfg.GoogleSearchEngineID == "" {
		fmt.Println("Error: Worker Google Search API not configured")
		fmt.Println("Please set GOOGLE_API_KEY_WORKER and GOOGLE_SEARCH_ENGINE_ID_WORKER")
		os.Exit(1)
	}

	fmt.Printf("Worker API Key: %s... (Engine ID: %s)\n\n", cfg.GoogleAPIKey[:20], cfg.GoogleSearchEngineID)
	toolRunner := tools.NewToolRunner(cfg)

	testQueries := []string{
		"golang concurrency",
		"日本の歴史",
		"machine learning basics",
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
