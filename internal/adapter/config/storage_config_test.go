package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeStorageTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigStoragePathsAreCanonical(t *testing.T) {
	path := writeStorageTestConfig(t, `
server:
  port: 8080
storage:
  memory:
    session_dir: "/state/sessions"
    operation_memory_dir: "/state/memory"
    cold_export_dir: "/state/exports/parquet"
  databases:
    conversation_l1: "/state/l1_memory.db"
    conversation_archive: "/state/memory_archive.db"
    tool_registry: "/state/tool_registry.db"
    glossary: "/state/glossary.db"
    advisor: "/state/workspace/logs/advisor.db"
    knowledge_memory: "/state/workspace/logs/knowledge_memory.db"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Session.StorageDir != "/state/sessions" ||
		cfg.OperationMemoryDir != "/state/memory" ||
		cfg.Capability.ToolRegistryDB != "/state/tool_registry.db" ||
		cfg.Glossary.DBPath != "/state/glossary.db" ||
		cfg.Advisor.SQLitePath != "/state/workspace/logs/advisor.db" ||
		cfg.KnowledgeMemory.SQLitePath != "/state/workspace/logs/knowledge_memory.db" {
		t.Fatalf("canonical storage paths were not applied: %+v", cfg.Storage)
	}
}

func TestLoadConfigBackupRequiresCompleteDestinations(t *testing.T) {
	path := writeStorageTestConfig(t, `
server:
  port: 8080
backup:
  core_source: "/state"
  core_snapshot_root: "/backup/core-snapshots"
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "backup.knowledge_source") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfigBackupRejectsMemoryOutsideCoreSource(t *testing.T) {
	path := writeStorageTestConfig(t, `
server:
  port: 8080
storage:
  memory:
    session_dir: "/outside/sessions"
    operation_memory_dir: "/state/memory"
    cold_export_dir: "/state/exports/parquet"
backup:
  core_source: "/state"
  core_snapshot_root: "/backup/core-snapshots"
  knowledge_source: "/knowledge"
  knowledge_mirror: "/mirror/knowledge"
  knowledge_versions: "/mirror/versions"
  recent_keep: 28
  daily_keep: 14
  weekly_keep: 8
  monthly_keep: 12
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "storage.memory.session_dir must be inside backup.core_source") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfigBackupRequiredMemoryExportsMustBeConfigured(t *testing.T) {
	path := writeStorageTestConfig(t, `
server:
  port: 8080
storage:
  memory:
    session_dir: "/state/sessions"
    operation_memory_dir: "/state/memory"
    cold_export_dir: "/state/exports/parquet"
backup:
  core_source: "/state"
  core_snapshot_root: "/backup/core-snapshots"
  knowledge_source: "/knowledge"
  knowledge_mirror: "/mirror/knowledge"
  knowledge_versions: "/mirror/versions"
  recent_keep: 28
  daily_keep: 14
  weekly_keep: 8
  monthly_keep: 12
  memory:
    require_exports: true
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "backup.memory.redis.enabled") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}
