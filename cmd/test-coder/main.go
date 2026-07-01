package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	llmfactory "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/factory"
)

func main() {
	// コマンドライン引数でCoderタイプとタスクを受け取る
	if len(os.Args) < 3 {
		fmt.Println("Usage: test-coder <coder-type> <task-description>")
		fmt.Println("Example: test-coder deepseek 'hello.goにHello World関数を追加'")
		fmt.Println("Example: test-coder openai 'main.goにロギング機能を追加'")
		fmt.Println("Example: test-coder claude 'pkg/test/にユニットテストを追加'")
		fmt.Println("Example: test-coder gemini 'CLIツールのプロトタイプを提案'")
		os.Exit(1)
	}
	coderType := os.Args[1]
	taskDescription := os.Args[2]

	// 設定読み込み
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()

	// Coderタイプに応じてプロバイダー選択
	coderCfg, coderName, err := resolveCoderConfig(cfg, coderType)
	if err != nil {
		log.Fatal(err)
	}
	provider, err := llmfactory.CreateProvider(coderCfg)
	if err != nil {
		log.Fatalf("Failed to create provider for %s: %v", coderName, err)
	}
	coder := agent.NewCoderAgent(provider, nil, nil, cfg.Prompts.CoderProposal)

	// Task作成
	jobID := task.NewJobID()
	t := task.NewTask(jobID, taskDescription, "cli", "test-user")

	fmt.Printf("🤖 Coder: %s\n", coderName)
	fmt.Printf("📝 Task: %s\n", taskDescription)
	fmt.Println("⏳ Generating Proposal...")

	// Proposal生成
	proposal, err := coder.GenerateProposal(ctx, t)
	if err != nil {
		log.Fatalf("❌ Failed to generate proposal: %v", err)
	}

	// 結果表示
	sep := strings.Repeat("=", 60)
	fmt.Println("\n" + sep)
	fmt.Println("📋 PLAN")
	fmt.Println(sep)
	fmt.Println(proposal.Plan())

	fmt.Println("\n" + sep)
	fmt.Println("🔧 PATCH")
	fmt.Println(sep)
	fmt.Println(proposal.Patch())

	fmt.Println("\n" + sep)
	fmt.Println("⚠️  RISK")
	fmt.Println(sep)
	fmt.Println(proposal.Risk())

	if proposal.CostHint() != "" {
		fmt.Println("\n" + sep)
		fmt.Println("💰 COST HINT")
		fmt.Println(sep)
		fmt.Println(proposal.CostHint())
	}

	fmt.Println("\n✅ Proposal generated successfully!")
}

func loadConfig() (*config.Config, error) {
	if path := os.Getenv("PICOCLAW_CONFIG"); path != "" {
		loadEnvFile(filepath.Join(filepath.Dir(path), ".env"))
		return config.LoadConfig(path)
	}
	if _, err := os.Stat("./config.yaml"); err == nil {
		loadEnvFile("./.env")
		return config.LoadConfig("./config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	loadEnvFile(filepath.Join(home, ".picoclaw", ".env"))
	return config.LoadConfig(filepath.Join(home, ".picoclaw", "config.yaml"))
}

func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func resolveCoderConfig(cfg *config.Config, coderType string) (config.CoderConfig, string, error) {
	switch coderType {
	case "deepseek", "coder1":
		cc := cfg.Coder1
		cc.Provider = firstNonEmpty(cc.Provider, "deepseek")
		cc.Model = firstNonEmpty(cc.Model, cfg.DeepSeek.Model)
		cc.APIKey = firstNonEmpty(os.Getenv("DEEPSEEK_API_KEY"), cc.APIKey, cfg.DeepSeek.APIKey)
		cc.Enabled = true
		return cc, "Coder1 (DeepSeek)", nil

	case "openai", "coder2":
		cc := cfg.Coder2
		cc.Provider = firstNonEmpty(cc.Provider, "openai")
		cc.Model = firstNonEmpty(cc.Model, cfg.OpenAI.Model)
		cc.APIKey = firstNonEmpty(os.Getenv("OPENAI_API_KEY"), cc.APIKey, cfg.OpenAI.APIKey)
		cc.Enabled = true
		return cc, "Coder2 (OpenAI)", nil

	case "claude", "coder3":
		cc := cfg.Coder3
		cc.Provider = firstNonEmpty(cc.Provider, "claude")
		cc.Model = firstNonEmpty(cc.Model, cfg.Claude.Model)
		cc.APIKey = firstNonEmpty(os.Getenv("ANTHROPIC_API_KEY"), cc.APIKey, cfg.Claude.APIKey)
		cc.Enabled = true
		return cc, "Coder3 (Claude)", nil

	case "gemini", "google", "coder4":
		cc := cfg.Coder4
		cc.Provider = firstNonEmpty(cc.Provider, "gemini")
		cc.APIKey = firstNonEmpty(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY"), cc.APIKey)
		cc.Enabled = true
		return cc, "Coder4 (Gemini)", nil

	default:
		return config.CoderConfig{}, "", fmt.Errorf("unknown coder type: %s (use: deepseek/coder1, openai/coder2, claude/coder3, gemini/coder4)", coderType)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
