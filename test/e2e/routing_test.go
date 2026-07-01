//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/openai"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/mcp"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/session"
	infrarouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

// coderAdapter は main.go と同じアダプター
type coderAdapter struct {
	domainCoder *agent.CoderAgent
}

func (a *coderAdapter) Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	return a.domainCoder.GenerateWithPrompt(ctx, t, systemPrompt)
}

// buildOrchestrator は本番同等の Orchestrator を config.yaml から構築する
func buildOrchestrator(t *testing.T, cfg *config.Config) *orchestrator.MessageOrchestrator {
	t.Helper()

	ollamaProvider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	classifier := infrarouting.NewLLMClassifier(ollamaProvider, cfg.Prompts.Classifier)
	ruleDictionary := infrarouting.NewRuleDictionary()

	chatToolRunner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
	})
	mcpClient := mcp.NewMCPClient()

	mioAgent := agent.NewMioAgent(ollamaProvider, classifier, ruleDictionary, chatToolRunner, mcpClient, nil)

	// Coder1 (DeepSeek) — CODE ルートのデフォルト
	var coder1 orchestrator.CoderAgent
	if cfg.DeepSeek.APIKey != "" {
		dp := deepseek.NewDeepSeekProvider(cfg.DeepSeek.APIKey, cfg.DeepSeek.Model)
		dc := agent.NewCoderAgent(dp, nil, nil, cfg.Prompts.CoderProposal)
		coder1 = &coderAdapter{domainCoder: dc}
	}

	// Coder2 (OpenAI)
	var coder2 orchestrator.CoderAgent
	if cfg.OpenAI.APIKey != "" {
		op := openai.NewOpenAIProvider(cfg.OpenAI.APIKey, cfg.OpenAI.Model)
		dc := agent.NewCoderAgent(op, nil, nil, cfg.Prompts.CoderProposal)
		coder2 = &coderAdapter{domainCoder: dc}
	}

	sessionRepo := session.NewJSONSessionRepository(t.TempDir())
	workerExec := service.NewWorkerExecutionService(cfg.Worker)

	return orchestrator.NewMessageOrchestrator(
		sessionRepo, mioAgent, nil,
		coder1, coder2, nil, nil,
		workerExec,
	)
}

// TestE2E_Routing_ChromeKeywords_CODE3 はChrome操作キーワードがルール辞書でCODE3に
// ルーティングされることを検証する（本番同等の MioAgent.DecideAction 経由）。
func TestE2E_Routing_ChromeKeywords_CODE3(t *testing.T) {
	cfg := getConfig(t)

	ollamaProvider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	classifier := infrarouting.NewLLMClassifier(ollamaProvider, cfg.Prompts.Classifier)
	ruleDictionary := infrarouting.NewRuleDictionary()

	chatToolRunner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
	})
	mcpClient := mcp.NewMCPClient()
	mioAgent := agent.NewMioAgent(ollamaProvider, classifier, ruleDictionary, chatToolRunner, mcpClient, nil)

	tests := []struct {
		name    string
		message string
	}{
		{"Chrome操作", "Chromeでこのページのデータを取得して"},
		{"ブラウザ操作", "ブラウザで検索結果をスクレイピングして"},
		{"画面操作", "画面操作でフォームに入力して送信して"},
		{"スクレイピング", "このサイトをスクレイピングしてCSVにして"},
		{"明示コマンド/code3", "/code3 このコードをレビューして"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			jobID := task.NewJobID()
			tsk := task.NewTask(jobID, tt.message, "test", "e2e-user")
			decision, err := mioAgent.DecideAction(ctx, tsk)
			if err != nil {
				t.Fatalf("DecideAction failed: %v", err)
			}

			if decision.Route != routing.RouteCODE3 {
				t.Errorf("route: want CODE3, got %s (reason: %s)", decision.Route, decision.Reason)
			}
			t.Logf("Route: %s (confidence: %.2f, reason: %s)", decision.Route, decision.Confidence, decision.Reason)
		})
	}
}

// TestE2E_Routing_FallbackChain_Coder1ToCoder2 はCODEルートでCoder1がnil時に
// Coder2（OpenAI）にフォールバックすることを検証する。
func TestE2E_Routing_FallbackChain_Coder1ToCoder2(t *testing.T) {
	cfg := getConfig(t)
	if cfg.OpenAI.APIKey == "" {
		t.Skip("OpenAI API key not configured")
	}

	ollamaProvider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	classifier := infrarouting.NewLLMClassifier(ollamaProvider, cfg.Prompts.Classifier)
	ruleDictionary := infrarouting.NewRuleDictionary()

	chatToolRunner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
	})
	mcpClient := mcp.NewMCPClient()
	mioAgent := agent.NewMioAgent(ollamaProvider, classifier, ruleDictionary, chatToolRunner, mcpClient, nil)

	// Coder2 (OpenAI) のみ設定、Coder1 = nil
	op := openai.NewOpenAIProvider(cfg.OpenAI.APIKey, cfg.OpenAI.Model)
	dc := agent.NewCoderAgent(op, nil, nil, cfg.Prompts.CoderProposal)
	coder2 := &coderAdapter{domainCoder: dc}

	sessionRepo := session.NewJSONSessionRepository(t.TempDir())
	workerExec := service.NewWorkerExecutionService(cfg.Worker)

	orch := orchestrator.NewMessageOrchestrator(
		sessionRepo, mioAgent, nil,
		nil, coder2, nil, nil, // coder1=nil → coder2にフォールバック
		workerExec,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-fallback",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "GoでHello Worldを実装して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE {
		t.Errorf("route: want CODE, got %s", resp.Route)
	}
	if resp.Response == "" {
		t.Error("expected non-empty response from Coder2 (fallback)")
	}
	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 500 chars): %.500s", resp.Response)
}

// TestE2E_Routing_Code_NaturalLanguage は自然言語でルーター（ルール辞書）を通し、
// CODE ルートに分類されて Coder1 (DeepSeek) が応答することを検証する。
func TestE2E_Routing_Code_NaturalLanguage(t *testing.T) {
	cfg := getConfig(t)
	if cfg.DeepSeek.APIKey == "" {
		t.Skip("DeepSeek API key not configured")
	}

	orch := buildOrchestrator(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-code-natural",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "Goでfizzbuzzの関数を作って、テストを追加して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE {
		t.Errorf("route: want CODE, got %s", resp.Route)
	}
	if resp.Response == "" {
		t.Error("expected non-empty response from Coder")
	}
	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 500 chars): %.500s", resp.Response)
}

// TestE2E_Routing_Chat_NaturalLanguage は日常会話がルーターで CHAT に分類され、
// Ollama (Mio) が応答することを検証する。
func TestE2E_Routing_Chat_NaturalLanguage(t *testing.T) {
	cfg := getConfig(t)
	requireOllamaReachable(t, cfg.Ollama.BaseURL)

	orch := buildOrchestrator(t, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := orch.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   "e2e-test-chat-natural",
		Channel:     "test",
		ChatID:      "e2e-user",
		UserMessage: "こんにちは、今日の天気はどうですか？",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCHAT {
		t.Errorf("route: want CHAT, got %s", resp.Route)
	}
	if resp.Response == "" {
		t.Error("expected non-empty chat response")
	}
	t.Logf("Route: %s (confidence: %.2f)", resp.Route, resp.Confidence)
	t.Logf("Response (first 500 chars): %.500s", resp.Response)
}
