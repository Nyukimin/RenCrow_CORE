package main

import (
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBackupConfigValues(t *testing.T) {
	backup := config.BackupConfig{
		CoreSource:        "/state",
		CoreSnapshotRoot:  "/backup/snapshots",
		KnowledgeSource:   "/knowledge",
		KnowledgeMirror:   "/mirror/knowledge",
		KnowledgeVersions: "/mirror/versions",
		RecentKeep:        28,
		DailyKeep:         14,
		WeeklyKeep:        8,
		MonthlyKeep:       12,
		Memory: config.MemoryBackupConfig{
			RequireExports: true,
			Redis: config.RedisBackupConfig{
				Enabled:   true,
				Container: "rencrow-redis",
			},
			Qdrant: config.QdrantBackupConfig{
				Enabled: true,
				BaseURL: "http://127.0.0.1:6333",
			},
		},
	}
	memory := config.MemoryStorageConfig{
		SessionDir:         "/state/sessions",
		OperationMemoryDir: "/state/memory",
		ColdExportDir:      "/state/exports/parquet",
		RawSourceDir:       "/state/raw-source",
	}
	databases := config.DatabasePathsConfig{
		ConversationL1:      "/state/l1_memory.db",
		ConversationArchive: "/state/memory_archive.db",
	}
	server := config.ServerConfig{Port: 18790}
	want := []string{
		"/state",
		"/backup/snapshots",
		"/knowledge",
		"/mirror/knowledge",
		"/mirror/versions",
		"28",
		"14",
		"8",
		"12",
		"/state/sessions",
		"/state/memory",
		"/state/exports/parquet",
		"/state/raw-source",
		"true",
		"true",
		"rencrow-redis",
		"true",
		"http://127.0.0.1:6333",
		"/state/l1_memory.db",
		"/state/memory_archive.db",
		"18790",
	}
	if got := backupConfigValues(backup, memory, databases, server); !reflect.DeepEqual(got, want) {
		t.Fatalf("backupConfigValues() = %#v, want %#v", got, want)
	}
}
