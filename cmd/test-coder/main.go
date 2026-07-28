package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
)

func main() {
	// コマンドライン引数でCoderタイプとタスクを受け取る
	if len(os.Args) < 3 {
		fmt.Println("Usage: test-coder <coder-type> <task-description>")
		fmt.Println("Example: test-coder coder1 'hello.goにHello World関数を追加'")
		fmt.Println("Example: test-coder coder2 'main.goにロギング機能を追加'")
		fmt.Println("Example: test-coder coder3 'pkg/test/にユニットテストを追加'")
		fmt.Println("Example: test-coder coder4 'CLIツールのプロトタイプを提案'")
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
	alias, coderName, err := resolveCoderAlias(coderType)
	if err != nil {
		log.Fatal(err)
	}
	apiKey := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		apiKey = strings.TrimSpace(os.Getenv(envName))
	}
	provider := rencrowllm.NewGatewayProviderWithOptions(
		apiKey,
		alias,
		cfg.LLMGateway.BaseURL,
		time.Duration(cfg.LLMGateway.TimeoutSec)*time.Second,
	)
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
	if path := os.Getenv("RENCROW_CONFIG"); path != "" {
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
	loadEnvFile(filepath.Join(home, ".rencrow", ".env"))
	return config.LoadConfig(filepath.Join(home, ".rencrow", "config.yaml"))
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

func resolveCoderAlias(coderType string) (string, string, error) {
	switch coderType {
	case "coder1":
		return "coder1", "Coder1 / Aka", nil

	case "coder2":
		return "coder2", "Coder2 / Ao", nil

	case "coder3":
		return "coder3", "Coder3 / Kin", nil

	case "coder4":
		return "coder4", "Coder4 / Gin", nil

	default:
		return "", "", fmt.Errorf("unknown coder type: %s (use coder1, coder2, coder3, or coder4)", coderType)
	}
}
