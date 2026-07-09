//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// getConfig は本番と同じ経路で config を読み込む
// .env → 環境変数 → config.yaml ${ENV_VAR} 展開 → Config struct
func getConfig(t *testing.T) *config.Config {
	t.Helper()
	configPath := os.Getenv("RENCROW_CONFIG")
	if configPath == "" {
		configPath = firstExistingPath(t,
			"../../config/config.yaml",
			"../../config/config.yaml.example",
			"config/config.yaml",
			"config/config.yaml.example",
		)
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	return cfg
}

func firstExistingPath(t *testing.T, candidates ...string) string {
	t.Helper()
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("no config file found in candidates: %s", strings.Join(candidates, ", "))
	return ""
}

func requireOllamaReachable(t *testing.T, baseURL string) {
	t.Helper()
	if strings.TrimSpace(baseURL) == "" {
		t.Skip("ollama base_url is empty")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/api/tags")
	if err != nil {
		t.Skipf("ollama is not reachable at %s: %v", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Skipf("ollama is not ready at %s: status=%d", baseURL, resp.StatusCode)
	}
}

func TestE2E_GoogleSearch_Chat(t *testing.T) {
	cfg := getConfig(t)

	if cfg.GoogleSearchChat.APIKey == "" || cfg.GoogleSearchChat.SearchEngineID == "" {
		t.Skip("google_search_chat not configured (APIKey or SearchEngineID empty)")
	}

	runner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, "web_search", map[string]interface{}{
		"query": "Go言語 testing パッケージ",
	})
	if err != nil {
		t.Fatalf("Chat web_search failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty search result")
	}
	if !strings.Contains(result, "http") {
		t.Error("expected search result to contain URLs")
	}
	t.Logf("Chat search result (first 300 chars): %.300s", result)
}

func TestE2E_GoogleSearch_Worker(t *testing.T) {
	cfg := getConfig(t)

	if cfg.GoogleSearchWorker.APIKey == "" || cfg.GoogleSearchWorker.SearchEngineID == "" {
		t.Skip("google_search_worker not configured (APIKey or SearchEngineID empty)")
	}

	runner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchWorker.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchWorker.SearchEngineID,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result, err := runner.Execute(ctx, "web_search", map[string]interface{}{
		"query": "Go言語 最新ニュース 2026",
	})
	if err != nil {
		t.Fatalf("Worker web_search failed: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty search result")
	}
	t.Logf("Worker search result (first 300 chars): %.300s", result)
}
