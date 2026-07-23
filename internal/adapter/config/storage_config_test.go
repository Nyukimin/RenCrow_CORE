package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig_StorageDatabasePathsAreCanonical(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "/state/sessions"
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
    movie_catalog: "/state/data/movie_catalog/eiga_catalog.sqlite"
    hobby_graph: "/state/data/hobby_graph/hobby_graph.sqlite"
    investment: "/state/data/investment/rencrow.db"
    advisor: "/state/workspace/logs/advisor.db"
    sandbox: "/state/workspace/logs/sandbox.db"
    dci: "/state/workspace/dci.db"
    skill_governance: "/state/workspace/logs/skill_governance.db"
    workstream: "/state/workspace/logs/workstream.db"
    revenue: "/state/workspace/logs/revenue.db"
    persona_architecture: "/state/workspace/logs/persona.db"
    browser_trace_to_api: "/state/workspace/browser_trace_to_api.db"
    complexity_hotspot: "/state/workspace/logs/complexity_hotspot.db"
    super_agent_harness: "/state/workspace/logs/superagent_harness.db"
    ai_workflow: "/state/workspace/logs/ai_workflow.db"
    knowledge_memory: "/state/workspace/logs/knowledge_memory.db"
backup:
  core_source: "/state"
  core_snapshot_root: "/backup/core-snapshots"
  knowledge_source: "/knowledge/RenCrowKnowledge"
  knowledge_mirror: "/backup-mirror/knowledge"
  knowledge_versions: "/backup-mirror/knowledge-versions"
  recent_keep: 28
  daily_keep: 14
  weekly_keep: 8
  monthly_keep: 12
  memory:
    require_exports: true
    redis:
      enabled: true
      container: "rencrow-redis"
    qdrant:
      enabled: true
      base_url: "http://127.0.0.1:6333"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Conversation.L1SQLitePath != "/state/l1_memory.db" {
		t.Fatalf("conversation L1 path = %q", cfg.Conversation.L1SQLitePath)
	}
	if cfg.Conversation.ArchiveSQLitePath != "/state/memory_archive.db" {
		t.Fatalf("conversation archive path = %q", cfg.Conversation.ArchiveSQLitePath)
	}
	if cfg.Capability.ToolRegistryDB != "/state/tool_registry.db" {
		t.Fatalf("tool registry path = %q", cfg.Capability.ToolRegistryDB)
	}
	if cfg.Glossary.DBPath != "/state/glossary.db" {
		t.Fatalf("glossary path = %q", cfg.Glossary.DBPath)
	}
	if cfg.Storage.Databases.MovieCatalog != "/state/data/movie_catalog/eiga_catalog.sqlite" {
		t.Fatalf("movie catalog path = %q", cfg.Storage.Databases.MovieCatalog)
	}
	if cfg.Advisor.SQLitePath != "/state/workspace/logs/advisor.db" {
		t.Fatalf("advisor path = %q", cfg.Advisor.SQLitePath)
	}
	if cfg.KnowledgeMemory.SQLitePath != "/state/workspace/logs/knowledge_memory.db" {
		t.Fatalf("knowledge memory path = %q", cfg.KnowledgeMemory.SQLitePath)
	}
	if cfg.Backup.CoreSnapshotRoot != "/backup/core-snapshots" {
		t.Fatalf("backup root = %q", cfg.Backup.CoreSnapshotRoot)
	}
	if cfg.Session.StorageDir != "/state/sessions" {
		t.Fatalf("session directory = %q", cfg.Session.StorageDir)
	}
	if cfg.OperationMemoryDir != "/state/memory" {
		t.Fatalf("operation memory directory = %q", cfg.OperationMemoryDir)
	}
	if cfg.Storage.Memory.ColdExportDir != "/state/exports/parquet" {
		t.Fatalf("cold export directory = %q", cfg.Storage.Memory.ColdExportDir)
	}
}

func TestLoadConfig_StorageDatabasePathConflictIsRejected(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./sessions"
conversation:
  l1_sqlite_path: "/legacy/l1.db"
storage:
  databases:
    conversation_l1: "/state/l1.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "storage.databases.conversation_l1") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfig_BackupConfigRequiresCompleteDestinations(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./sessions"
backup:
  core_source: "/state"
  core_snapshot_root: "/backup/core-snapshots"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "backup.knowledge_source") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfig_StorageMemoryPathConflictIsRejected(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "/legacy/sessions"
storage:
  memory:
    session_dir: "/state/sessions"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "storage.memory.session_dir") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfig_BackupRejectsMemoryOutsideCoreSource(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
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
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "storage.memory.session_dir must be inside backup.core_source") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}

func TestLoadConfig_BackupRequiredMemoryExportsMustBeConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
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
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(configPath)
	if err == nil || !strings.Contains(err.Error(), "backup.memory.redis.enabled") {
		t.Fatalf("LoadConfig error = %v", err)
	}
}
