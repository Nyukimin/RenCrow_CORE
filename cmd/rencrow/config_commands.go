package main

import (
	"fmt"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func cmdConfig() {
	if len(os.Args) < 3 || os.Args[2] != "backup-values" {
		fmt.Fprintln(os.Stderr, "usage: rencrow config backup-values")
		os.Exit(2)
	}
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	for _, value := range backupConfigValues(cfg.Backup, cfg.Storage.Memory, cfg.Storage.Databases) {
		fmt.Println(value)
	}
}

func backupConfigValues(backup config.BackupConfig, memory config.MemoryStorageConfig, databases config.DatabasePathsConfig) []string {
	return []string{
		backup.CoreSource,
		backup.CoreSnapshotRoot,
		backup.KnowledgeSource,
		backup.KnowledgeMirror,
		backup.KnowledgeVersions,
		fmt.Sprintf("%d", backup.RecentKeep),
		fmt.Sprintf("%d", backup.DailyKeep),
		fmt.Sprintf("%d", backup.WeeklyKeep),
		fmt.Sprintf("%d", backup.MonthlyKeep),
		memory.SessionDir,
		memory.OperationMemoryDir,
		memory.ColdExportDir,
		fmt.Sprintf("%t", backup.Memory.RequireExports),
		fmt.Sprintf("%t", backup.Memory.Redis.Enabled),
		backup.Memory.Redis.Container,
		fmt.Sprintf("%t", backup.Memory.Qdrant.Enabled),
		backup.Memory.Qdrant.BaseURL,
		databases.ConversationL1,
		databases.ConversationArchive,
	}
}
