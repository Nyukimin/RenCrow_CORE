package main

import (
	"path/filepath"
	"reflect"
	"strings"
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
		CharImageHistory:    "/state/databases/charimage_history.db",
	}
	server := config.ServerConfig{Port: 18790}
	workspaceDir := filepath.Join(string(filepath.Separator), "workspace")
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
		// The Task store and the Action store live under workspace_dir, at the
		// places the runtime and the CLIs open them (not under backup.core_source).
		workspaceDir,
		defaultTaskStorePath(workspaceDir),
		defaultActionStorePath(workspaceDir),
		"/state/databases/charimage_history.db",
	}
	if got := backupConfigValues(backup, memory, databases, server, workspaceDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("backupConfigValues() = %#v, want %#v", got, want)
	}
}

// The backup runner reads these three values by position and archives the two
// stores as a mirror of their place under workspace_dir, so the stores must stay
// inside it, one directory each.
func TestBackupConfigValuesPlaceBothStoresUnderWorkspaceDir(t *testing.T) {
	workspaceDir := filepath.Join(string(filepath.Separator), "srv", "workspace")
	databases := config.DatabasePathsConfig{CharImageHistory: filepath.Join(string(filepath.Separator), "srv", "db", "core", "charimage_history.db")}
	values := backupConfigValues(config.BackupConfig{}, config.MemoryStorageConfig{}, databases, config.ServerConfig{}, workspaceDir)
	if len(values) != 25 {
		t.Fatalf("backup-values has %d values, the backup runner expects 25", len(values))
	}
	for _, store := range values[22:24] {
		relative, err := filepath.Rel(values[21], store)
		if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("store %q is not strictly inside workspace_dir %q", store, values[21])
		}
	}
	if values[22] == values[23] {
		t.Fatalf("the Task store and the Action store share a root: %q", values[22])
	}
	if values[24] != databases.CharImageHistory {
		t.Fatalf("CharImage history path=%q, want configured value %q", values[24], databases.CharImageHistory)
	}
}

// An unset workspace_dir has no stores: the values stay empty so that the
// backup runner stops before it touches CORE, instead of archiving a guess.
func TestBackupConfigValuesLeaveStoresEmptyWithoutWorkspaceDir(t *testing.T) {
	values := backupConfigValues(config.BackupConfig{}, config.MemoryStorageConfig{}, config.DatabasePathsConfig{}, config.ServerConfig{}, "")
	if len(values) != 25 || values[21] != "" || values[22] != "" || values[23] != "" || values[24] != "" {
		t.Fatalf("backup values = %#v, want four unset values after workspace_dir", values[21:])
	}
}
