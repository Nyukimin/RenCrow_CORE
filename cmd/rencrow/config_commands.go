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
	for _, value := range backupConfigValues(cfg.Backup, cfg.Storage.Memory, cfg.Storage.Databases, cfg.Server, cfg.WorkspaceDir) {
		fmt.Println(value)
	}
}

func backupConfigValues(backup config.BackupConfig, memory config.MemoryStorageConfig, databases config.DatabasePathsConfig, server config.ServerConfig, workspaceDir string) []string {
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
		memory.RawSourceDir,
		fmt.Sprintf("%t", backup.Memory.RequireExports),
		fmt.Sprintf("%t", backup.Memory.Redis.Enabled),
		backup.Memory.Redis.Container,
		fmt.Sprintf("%t", backup.Memory.Qdrant.Enabled),
		backup.Memory.Qdrant.BaseURL,
		databases.ConversationL1,
		databases.ConversationArchive,
		fmt.Sprintf("%d", server.Port),
		// The Task store and the Action store are not under backup.core_source:
		// they sit under workspace_dir, where the runtime and the storage CLIs open
		// them. The paths come from the functions those CLIs use, so the backup
		// never keeps a second copy of where the stores are.
		workspaceDir,
		defaultTaskStorePath(workspaceDir),
		defaultActionStorePath(workspaceDir),
		databases.CharImageHistory,
	}
}
