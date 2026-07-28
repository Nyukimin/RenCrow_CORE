package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func (c *Config) applyCanonicalStoragePaths() error {
	applyRuntimePath(c.Storage.Memory.SessionDir, &c.Session.StorageDir)
	applyRuntimePath(c.Storage.Memory.OperationMemoryDir, &c.OperationMemoryDir)

	mappings := []struct {
		canonical string
		runtime   *string
	}{
		{c.Storage.Databases.ToolRegistry, &c.Capability.ToolRegistryDB},
		{c.Storage.Databases.Glossary, &c.Glossary.DBPath},
		{c.Storage.Databases.Advisor, &c.Advisor.SQLitePath},
		{c.Storage.Databases.Sandbox, &c.Sandbox.SQLitePath},
		{c.Storage.Databases.DCI, &c.DCI.SQLitePath},
		{c.Storage.Databases.SkillGovernance, &c.SkillGovernance.SQLitePath},
		{c.Storage.Databases.Workstream, &c.Workstream.SQLitePath},
		{c.Storage.Databases.Revenue, &c.Revenue.SQLitePath},
		{c.Storage.Databases.PersonaArchitecture, &c.PersonaArchitecture.SQLitePath},
		{c.Storage.Databases.BrowserTraceToAPI, &c.BrowserTraceToAPI.SQLitePath},
		{c.Storage.Databases.ComplexityHotspot, &c.ComplexityHotspot.SQLitePath},
		{c.Storage.Databases.SuperAgentHarness, &c.SuperAgentHarness.SQLitePath},
		{c.Storage.Databases.AIWorkflow, &c.AIWorkflow.SQLitePath},
		{c.Storage.Databases.KnowledgeMemory, &c.KnowledgeMemory.SQLitePath},
	}
	for _, mapping := range mappings {
		applyRuntimePath(mapping.canonical, mapping.runtime)
	}
	return nil
}

func applyRuntimePath(canonicalValue string, runtime *string) {
	canonical := strings.TrimSpace(canonicalValue)
	if canonical == "" {
		return
	}
	*runtime = canonical
}

func (c *Config) populateCanonicalStoragePaths() {
	if strings.TrimSpace(c.Storage.Memory.SessionDir) == "" {
		c.Storage.Memory.SessionDir = c.Session.StorageDir
	}
	if strings.TrimSpace(c.Storage.Memory.OperationMemoryDir) == "" {
		c.Storage.Memory.OperationMemoryDir = c.OperationMemoryDir
	}
	if strings.TrimSpace(c.Storage.Databases.ToolRegistry) == "" {
		c.Storage.Databases.ToolRegistry = c.Capability.ToolRegistryDB
	}
	if strings.TrimSpace(c.Storage.Databases.Glossary) == "" {
		c.Storage.Databases.Glossary = c.Glossary.DBPath
	}
	if strings.TrimSpace(c.Storage.Databases.Advisor) == "" {
		c.Storage.Databases.Advisor = c.Advisor.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.Sandbox) == "" {
		c.Storage.Databases.Sandbox = c.Sandbox.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.DCI) == "" {
		c.Storage.Databases.DCI = c.DCI.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.SkillGovernance) == "" {
		c.Storage.Databases.SkillGovernance = c.SkillGovernance.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.Workstream) == "" {
		c.Storage.Databases.Workstream = c.Workstream.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.Revenue) == "" {
		c.Storage.Databases.Revenue = c.Revenue.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.PersonaArchitecture) == "" {
		c.Storage.Databases.PersonaArchitecture = c.PersonaArchitecture.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.BrowserTraceToAPI) == "" {
		c.Storage.Databases.BrowserTraceToAPI = c.BrowserTraceToAPI.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.ComplexityHotspot) == "" {
		c.Storage.Databases.ComplexityHotspot = c.ComplexityHotspot.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.SuperAgentHarness) == "" {
		c.Storage.Databases.SuperAgentHarness = c.SuperAgentHarness.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.AIWorkflow) == "" {
		c.Storage.Databases.AIWorkflow = c.AIWorkflow.SQLitePath
	}
	if strings.TrimSpace(c.Storage.Databases.KnowledgeMemory) == "" {
		c.Storage.Databases.KnowledgeMemory = c.KnowledgeMemory.SQLitePath
	}
}

func (c BackupConfig) configured() bool {
	return strings.TrimSpace(c.CoreSource) != "" ||
		strings.TrimSpace(c.CoreSnapshotRoot) != "" ||
		strings.TrimSpace(c.KnowledgeSource) != "" ||
		strings.TrimSpace(c.KnowledgeMirror) != "" ||
		strings.TrimSpace(c.KnowledgeVersions) != "" ||
		c.RecentKeep != 0 ||
		c.DailyKeep != 0 ||
		c.WeeklyKeep != 0 ||
		c.MonthlyKeep != 0 ||
		c.Memory.RequireExports ||
		c.Memory.Redis.Enabled ||
		strings.TrimSpace(c.Memory.Redis.Container) != "" ||
		c.Memory.Qdrant.Enabled ||
		strings.TrimSpace(c.Memory.Qdrant.BaseURL) != ""
}

func (c *Config) validateBackupConfig() error {
	if !c.Backup.configured() {
		return nil
	}
	requiredPaths := []struct {
		key   string
		value string
	}{
		{"backup.core_source", c.Backup.CoreSource},
		{"backup.core_snapshot_root", c.Backup.CoreSnapshotRoot},
		{"backup.knowledge_source", c.Backup.KnowledgeSource},
		{"backup.knowledge_mirror", c.Backup.KnowledgeMirror},
		{"backup.knowledge_versions", c.Backup.KnowledgeVersions},
	}
	for _, required := range requiredPaths {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required when backup is configured", required.key)
		}
		if !configPathIsAbs(required.value) {
			return fmt.Errorf("%s must be an absolute path", required.key)
		}
	}
	for _, retention := range []struct {
		key   string
		value int
	}{
		{"backup.recent_keep", c.Backup.RecentKeep},
		{"backup.daily_keep", c.Backup.DailyKeep},
		{"backup.weekly_keep", c.Backup.WeeklyKeep},
		{"backup.monthly_keep", c.Backup.MonthlyKeep},
	} {
		if retention.value < 1 {
			return fmt.Errorf("%s must be >= 1", retention.key)
		}
	}

	for _, required := range []struct {
		key   string
		value string
	}{
		{"storage.memory.session_dir", c.Storage.Memory.SessionDir},
		{"storage.memory.operation_memory_dir", c.Storage.Memory.OperationMemoryDir},
		{"storage.memory.cold_export_dir", c.Storage.Memory.ColdExportDir},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required when backup is configured", required.key)
		}
	}
	if c.Backup.Memory.RequireExports {
		if !c.Backup.Memory.Redis.Enabled {
			return fmt.Errorf("backup.memory.redis.enabled must be true when backup.memory.require_exports is true")
		}
		if strings.TrimSpace(c.Backup.Memory.Redis.Container) == "" {
			return fmt.Errorf("backup.memory.redis.container is required when backup.memory.redis.enabled is true")
		}
		if !c.Backup.Memory.Qdrant.Enabled {
			return fmt.Errorf("backup.memory.qdrant.enabled must be true when backup.memory.require_exports is true")
		}
	}
	if c.Backup.Memory.Redis.Enabled && strings.TrimSpace(c.Backup.Memory.Redis.Container) == "" {
		return fmt.Errorf("backup.memory.redis.container is required when backup.memory.redis.enabled is true")
	}
	if c.Backup.Memory.Qdrant.Enabled {
		baseURL := strings.TrimSpace(c.Backup.Memory.Qdrant.BaseURL)
		parsed, err := url.Parse(baseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("backup.memory.qdrant.base_url must be an absolute HTTP URL when enabled=true")
		}
	}
	coreSource := filepath.Clean(c.Backup.CoreSource)
	storagePaths := []struct {
		key   string
		value string
	}{
		{"storage.memory.session_dir", c.Storage.Memory.SessionDir},
		{"storage.memory.operation_memory_dir", c.Storage.Memory.OperationMemoryDir},
		{"storage.memory.cold_export_dir", c.Storage.Memory.ColdExportDir},
		{"storage.databases.conversation_l1", c.Storage.Databases.ConversationL1},
		{"storage.databases.conversation_archive", c.Storage.Databases.ConversationArchive},
		{"storage.databases.tool_registry", c.Storage.Databases.ToolRegistry},
		{"storage.databases.glossary", c.Storage.Databases.Glossary},
		{"storage.databases.movie_catalog", c.Storage.Databases.MovieCatalog},
		{"storage.databases.hobby_graph", c.Storage.Databases.HobbyGraph},
		{"storage.databases.investment", c.Storage.Databases.Investment},
		{"storage.databases.advisor", c.Storage.Databases.Advisor},
		{"storage.databases.sandbox", c.Storage.Databases.Sandbox},
		{"storage.databases.dci", c.Storage.Databases.DCI},
		{"storage.databases.skill_governance", c.Storage.Databases.SkillGovernance},
		{"storage.databases.workstream", c.Storage.Databases.Workstream},
		{"storage.databases.revenue", c.Storage.Databases.Revenue},
		{"storage.databases.persona_architecture", c.Storage.Databases.PersonaArchitecture},
		{"storage.databases.browser_trace_to_api", c.Storage.Databases.BrowserTraceToAPI},
		{"storage.databases.complexity_hotspot", c.Storage.Databases.ComplexityHotspot},
		{"storage.databases.super_agent_harness", c.Storage.Databases.SuperAgentHarness},
		{"storage.databases.ai_workflow", c.Storage.Databases.AIWorkflow},
		{"storage.databases.knowledge_memory", c.Storage.Databases.KnowledgeMemory},
	}
	for _, configuredPath := range storagePaths {
		storagePath := strings.TrimSpace(configuredPath.value)
		if storagePath == "" {
			continue
		}
		withinCore, err := pathWithin(coreSource, storagePath)
		if err != nil || !withinCore {
			return fmt.Errorf("%s must be inside backup.core_source", configuredPath.key)
		}
	}
	return nil
}

// configPathIsAbs reports whether a configured path is absolute on any
// supported platform. config.yaml is shared across Windows, Linux, and macOS,
// so a POSIX path such as "/state" must validate on Windows too, where
// filepath.IsAbs requires a drive letter or a UNC prefix.
func configPathIsAbs(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return true
	}
	slash := filepath.ToSlash(p)
	if strings.HasPrefix(slash, "/") {
		return true
	}
	if len(slash) >= 3 && slash[1] == ':' && slash[2] == '/' {
		drive := slash[0]
		return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
	}
	return false
}

func pathWithin(root, path string) (bool, error) {
	if !configPathIsAbs(path) {
		return false, nil
	}
	relative, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil {
		return false, err
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}
