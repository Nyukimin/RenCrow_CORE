package main

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/chatgptimport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type chatGPTImportStartupReconcileResult struct {
	RemovedStages  int
	BlockedImports int
}

func reconcileChatGPTImportStartup(ctx context.Context, store *l1sqlite.L1SQLiteStore, rawRoot string) (chatGPTImportStartupReconcileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	removedStages, err := chatgptimport.ReconcileUploadStages(rawRoot)
	if err != nil {
		return chatGPTImportStartupReconcileResult{}, fmt.Errorf("reconcile ChatGPT upload stages: %w", err)
	}
	if err := store.SetCommonRawSourceRoot(rawRoot); err != nil {
		return chatGPTImportStartupReconcileResult{RemovedStages: removedStages}, fmt.Errorf("reconcile Common Raw source root: %w", err)
	}
	blockedImports, err := store.ReconcileActiveChatGPTImports(ctx)
	if err != nil {
		return chatGPTImportStartupReconcileResult{RemovedStages: removedStages}, fmt.Errorf("reconcile active ChatGPT imports: %w", err)
	}
	return chatGPTImportStartupReconcileResult{RemovedStages: removedStages, BlockedImports: blockedImports}, nil
}
