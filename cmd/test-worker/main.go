package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func main() {
	// コマンドライン引数でPatchファイルを受け取る
	if len(os.Args) < 2 {
		fmt.Println("Usage: test-worker <patch-file.json>")
		fmt.Println("Example: test-worker test_patch.json")
		os.Exit(1)
	}
	patchFile := os.Args[1]

	// 設定読み込み
	cfg, err := config.LoadConfig("./config.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// WorkerExecutionService
	workerService := service.NewWorkerExecutionService(cfg.Worker)

	// Patchファイル読み込み
	patchData, err := os.ReadFile(patchFile)
	if err != nil {
		log.Fatalf("Failed to read patch file: %v", err)
	}

	// Proposal作成
	prop := proposal.NewProposal(
		"Test Worker execution",
		string(patchData),
		"Low - test only",
		"",
	)

	// Worker実行
	ctx := context.Background()
	jobID := task.JobIDFromString("test-worker-001")

	fmt.Printf("📋 Patch: %s\n", patchFile)
	fmt.Printf("🆔 JobID: %s\n", jobID)
	fmt.Println("⏳ Executing...")

	result, err := workerService.ExecuteProposal(ctx, jobID, prop)
	if err != nil {
		log.Fatalf("Execution failed: %v", err)
	}

	// 結果表示
	statusStr := "Success"
	if !result.Success {
		statusStr = "Failed"
	}
	fmt.Printf("\n✅ Status: %s\n", statusStr)
	fmt.Printf("📊 Executed: %d commands\n", result.ExecutedCmds)
	fmt.Printf("❌ Failed: %d commands\n", result.FailedCmds)

	if result.GitCommit != "" {
		fmt.Printf("📝 Git Commit: %s\n", result.GitCommit)
	}

	fmt.Println("\n📄 Command Results:")
	for i, cmdResult := range result.Results {
		status := "✅"
		if !cmdResult.Success {
			status = "❌"
		}
		fmt.Printf("  %d. %s %s\n", i+1, status, cmdResult.Command.Type)
		if cmdResult.Error != "" {
			fmt.Printf("     Error: %s\n", cmdResult.Error)
		}
	}
}
