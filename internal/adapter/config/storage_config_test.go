package config

import (
	"fmt"
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
    raw_source_dir: "/state/raw-source"
  databases:
    conversation_l1: "/state/l1_memory.db"
    conversation_archive: "/state/memory_archive.db"
    tool_registry: "/state/tool_registry.db"
    glossary: "/state/glossary.db"
    event_store: "/state/events.db"
    advisor: "/state/workspace/logs/advisor.db"
    knowledge_memory: "/state/workspace/logs/knowledge_memory.db"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Session.StorageDir != "/state/sessions" ||
		cfg.OperationMemoryDir != "/state/memory" ||
		cfg.Storage.Memory.RawSourceDir != "/state/raw-source" ||
		cfg.Capability.ToolRegistryDB != "/state/tool_registry.db" ||
		cfg.Glossary.DBPath != "/state/glossary.db" ||
		cfg.Storage.Databases.EventStore != "/state/events.db" ||
		cfg.Advisor.SQLitePath != "/state/workspace/logs/advisor.db" ||
		cfg.KnowledgeMemory.SQLitePath != "/state/workspace/logs/knowledge_memory.db" {
		t.Fatalf("canonical storage paths were not applied: %+v", cfg.Storage)
	}
}

func TestLoadConfigRawSourceDirIsAbsoluteNonRootAndBackupCohortRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		root bool
	}{
		{name: "posix-root", path: "/", root: true},
		{name: "drive-root-slash", path: "C:/", root: true},
		{name: "drive-root-backslash", path: "C:\\", root: true},
		{name: "unc-root-slash", path: "//server/share", root: true},
		{name: "unc-root-backslash", path: "\\\\server\\share", root: true},
		{name: "drive-child", path: "C:/state/raw", root: false},
		{name: "unc-child", path: "\\\\server\\share\\state\\raw", root: false},
	} {
		t.Run("path-"+tc.name, func(t *testing.T) {
			if !configPathIsAbs(tc.path) {
				t.Fatalf("configPathIsAbs(%q)=false", tc.path)
			}
			if got := configPathIsFilesystemRoot(tc.path); got != tc.root {
				t.Fatalf("configPathIsFilesystemRoot(%q)=%v, want %v", tc.path, got, tc.root)
			}
		})
	}
	for _, tc := range []struct {
		name string
		root string
		path string
		want bool
	}{
		{name: "drive-child", root: "C:/state", path: "C:/state/raw", want: true},
		{name: "drive-outside", root: "C:/state", path: "D:/state/raw", want: false},
		{name: "unc-child", root: `\\server\share\state`, path: `\\server\share\state\raw`, want: true},
		{name: "unc-outside-share", root: `\\server\share\state`, path: `\\server\other\state\raw`, want: false},
	} {
		t.Run("within-"+tc.name, func(t *testing.T) {
			got, err := pathWithin(tc.root, tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("pathWithin(%q, %q)=(%v, %v), want (%v, nil)", tc.root, tc.path, got, err, tc.want)
			}
		})
	}

	baseConfig := func(raw string) *Config {
		return &Config{
			Storage: StorageConfig{
				Memory:    MemoryStorageConfig{SessionDir: "/state/sessions", OperationMemoryDir: "/state/memory", ColdExportDir: "/state/exports/parquet", RawSourceDir: raw},
				Databases: DatabasePathsConfig{ConversationL1: "/state/l1.db", ConversationArchive: "/state/l2.db"},
			},
			Backup: BackupConfig{CoreSource: "/state", CoreSnapshotRoot: "/backup/core-snapshots", KnowledgeSource: "/knowledge", KnowledgeMirror: "/mirror/knowledge", KnowledgeVersions: "/mirror/versions", RecentKeep: 28, DailyKeep: 14, WeeklyKeep: 8, MonthlyKeep: 12},
		}
	}
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "valid", raw: "/state/raw-source"},
		{name: "relative", raw: "raw-source", want: "storage.memory.raw_source_dir must be an absolute path"},
		{name: "filesystem-root", raw: "/", want: "storage.memory.raw_source_dir must not be a filesystem or volume root"},
		{name: "outside-core", raw: "/other/raw-source", want: "storage.memory.raw_source_dir must be inside backup.core_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := baseConfig(tc.raw).validateBackupConfig()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateBackupConfig failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateBackupConfig error=%v, want %q", err, tc.want)
			}
		})
	}
	if err := baseConfig("").validateBackupConfig(); err == nil || !strings.Contains(err.Error(), "storage.memory.raw_source_dir is required when backup is configured") {
		t.Fatalf("missing raw source dir error=%v", err)
	}
}

func TestLoadConfigOwnerStorageDefaultsPreferCanonicalSQLitePaths(t *testing.T) {
	type ownerStorageCase struct {
		name        string
		databaseKey string
		moduleKey   string
		storage     func(*Config) string
		sqlitePath  func(*Config) string
	}
	cases := []ownerStorageCase{
		{name: "advisor", databaseKey: "advisor", moduleKey: "advisor", storage: func(c *Config) string { return c.Advisor.Storage }, sqlitePath: func(c *Config) string { return c.Advisor.SQLitePath }},
		{name: "sandbox", databaseKey: "sandbox", moduleKey: "sandbox", storage: func(c *Config) string { return c.Sandbox.Storage }, sqlitePath: func(c *Config) string { return c.Sandbox.SQLitePath }},
		{name: "dci", databaseKey: "dci", moduleKey: "dci", storage: func(c *Config) string { return c.DCI.Storage }, sqlitePath: func(c *Config) string { return c.DCI.SQLitePath }},
		{name: "skill_governance", databaseKey: "skill_governance", moduleKey: "skill_governance", storage: func(c *Config) string { return c.SkillGovernance.Storage }, sqlitePath: func(c *Config) string { return c.SkillGovernance.SQLitePath }},
		{name: "workstream", databaseKey: "workstream", moduleKey: "workstream", storage: func(c *Config) string { return c.Workstream.Storage }, sqlitePath: func(c *Config) string { return c.Workstream.SQLitePath }},
		{name: "revenue", databaseKey: "revenue", moduleKey: "revenue", storage: func(c *Config) string { return c.Revenue.Storage }, sqlitePath: func(c *Config) string { return c.Revenue.SQLitePath }},
		{name: "persona_architecture", databaseKey: "persona_architecture", moduleKey: "persona_architecture", storage: func(c *Config) string { return c.PersonaArchitecture.Storage }, sqlitePath: func(c *Config) string { return c.PersonaArchitecture.SQLitePath }},
		{name: "browser_trace_to_api", databaseKey: "browser_trace_to_api", moduleKey: "browser_trace_to_api", storage: func(c *Config) string { return c.BrowserTraceToAPI.Storage }, sqlitePath: func(c *Config) string { return c.BrowserTraceToAPI.SQLitePath }},
		{name: "complexity_hotspot", databaseKey: "complexity_hotspot", moduleKey: "complexity_hotspot", storage: func(c *Config) string { return c.ComplexityHotspot.Storage }, sqlitePath: func(c *Config) string { return c.ComplexityHotspot.SQLitePath }},
		{name: "super_agent_harness", databaseKey: "super_agent_harness", moduleKey: "superagent_harness", storage: func(c *Config) string { return c.SuperAgentHarness.Storage }, sqlitePath: func(c *Config) string { return c.SuperAgentHarness.SQLitePath }},
		{name: "ai_workflow", databaseKey: "ai_workflow", moduleKey: "ai_workflow", storage: func(c *Config) string { return c.AIWorkflow.Storage }, sqlitePath: func(c *Config) string { return c.AIWorkflow.SQLitePath }},
		{name: "knowledge_memory", databaseKey: "knowledge_memory", moduleKey: "knowledge_memory", storage: func(c *Config) string { return c.KnowledgeMemory.Storage }, sqlitePath: func(c *Config) string { return c.KnowledgeMemory.SQLitePath }},
	}
	canonicalPaths := make(map[string]string, len(cases))
	for _, tc := range cases {
		canonicalPaths[tc.name] = filepath.Join(t.TempDir(), tc.name+".db")
	}

	writeConfig := func(t *testing.T, canonical bool, explicitStorage string) *Config {
		t.Helper()
		var content strings.Builder
		content.WriteString("server:\n  port: 8080\n")
		if canonical {
			content.WriteString("storage:\n  databases:\n")
			for _, tc := range cases {
				fmt.Fprintf(&content, "    %s: %q\n", tc.databaseKey, canonicalPaths[tc.name])
			}
		}
		if explicitStorage != "" {
			for _, tc := range cases {
				fmt.Fprintf(&content, "%s:\n  storage: %s\n", tc.moduleKey, explicitStorage)
			}
		}
		cfg, err := LoadConfig(writeStorageTestConfig(t, content.String()))
		if err != nil {
			t.Fatalf("LoadConfig failed: %v\n%s", err, content.String())
		}
		return cfg
	}

	noCanonical := writeConfig(t, false, "")
	for _, tc := range cases {
		t.Run(tc.name+"/no-canonical-default", func(t *testing.T) {
			if got := tc.storage(noCanonical); got != "jsonl" {
				t.Fatalf("Storage = %q, want jsonl", got)
			}
		})
	}

	canonicalDefault := writeConfig(t, true, "")
	for _, tc := range cases {
		t.Run(tc.name+"/canonical-default", func(t *testing.T) {
			if got := tc.storage(canonicalDefault); got != "sqlite" {
				t.Fatalf("Storage = %q, want sqlite", got)
			}
			if got := tc.sqlitePath(canonicalDefault); got != canonicalPaths[tc.name] || got == "" {
				t.Fatalf("SQLitePath = %q, want canonical path %q", got, canonicalPaths[tc.name])
			}
		})
	}

	explicitJSONL := writeConfig(t, true, "jsonl")
	for _, tc := range cases {
		t.Run(tc.name+"/explicit-jsonl", func(t *testing.T) {
			if got := tc.storage(explicitJSONL); got != "jsonl" {
				t.Fatalf("Storage = %q, want explicit jsonl", got)
			}
			if got := tc.sqlitePath(explicitJSONL); got != canonicalPaths[tc.name] {
				t.Fatalf("SQLitePath = %q, want canonical path %q", got, canonicalPaths[tc.name])
			}
		})
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
    raw_source_dir: "/state/raw-source"
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
    raw_source_dir: "/state/raw-source"
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
