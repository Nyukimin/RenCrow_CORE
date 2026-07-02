package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func userHomeDirForTest(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Fatalf("os.UserHomeDir failed: %v", err)
	}
	return home
}

func TestLoadConfig_Success(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080
  host: "0.0.0.0"

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"

log:
  level: "info"
  format: "json"
`

	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", cfg.Server.Port)
	}

	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Expected host '0.0.0.0', got '%s'", cfg.Server.Host)
	}

	if cfg.Ollama.BaseURL != "http://localhost:11434" {
		t.Errorf("Expected Ollama base URL, got '%s'", cfg.Ollama.BaseURL)
	}

	if cfg.Ollama.Model != "picoclaw-v1" {
		t.Errorf("Expected Ollama model 'picoclaw-v1', got '%s'", cfg.Ollama.Model)
	}

	if cfg.Session.StorageDir != "./data/sessions" {
		t.Errorf("Expected session storage dir, got '%s'", cfg.Session.StorageDir)
	}
}

func TestLoadConfig_ComfyUIDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configContent := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.ComfyUI.BaseURL != "http://100.83.207.6:8188" {
		t.Fatalf("ComfyUI base_url = %q", cfg.ComfyUI.BaseURL)
	}
	if cfg.ComfyUI.ClientID != "rencrow-server" {
		t.Fatalf("ComfyUI client_id = %q", cfg.ComfyUI.ClientID)
	}
	if cfg.ComfyUI.PollIntervalSec != 3 || cfg.ComfyUI.TimeoutSec != 300 {
		t.Fatalf("ComfyUI timing = poll %d timeout %d", cfg.ComfyUI.PollIntervalSec, cfg.ComfyUI.TimeoutSec)
	}
}

func TestLoadConfig_LineChannelPolicy(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
line:
  channel_secret: "secret"
  access_token: "token"
  channel_policy:
    enabled: true
    allow_dm: false
    allow_groups: true
    allowed_senders:
      - "U-allowed"
    paired_groups:
      - "G-paired"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	policy, ok := ResolveChannelPolicyConfig(cfg.Line.ChannelPolicy)
	if !ok {
		t.Fatal("expected channel policy to be enabled")
	}
	if policy.AllowDM {
		t.Fatal("expected allow_dm=false to be preserved")
	}
	if !policy.AllowGroups {
		t.Fatal("expected allow_groups=true to be preserved")
	}
	if len(policy.AllowedSenders) != 1 || policy.AllowedSenders[0] != "U-allowed" {
		t.Fatalf("unexpected allowed senders: %#v", policy.AllowedSenders)
	}
	if len(policy.PairedGroups) != 1 || policy.PairedGroups[0] != "G-paired" {
		t.Fatalf("unexpected paired groups: %#v", policy.PairedGroups)
	}
}

func TestResolveChannelPolicyConfigDefaultsAndTrim(t *testing.T) {
	policy, ok := ResolveChannelPolicyConfig(ChannelPolicyConfig{
		Enabled:        true,
		AllowedSenders: []string{" U1 ", "", "U2"},
		PairedGroups:   []string{" G1 ", "  "},
	})
	if !ok {
		t.Fatal("expected policy to be enabled")
	}
	if !policy.AllowDM {
		t.Fatal("default allow_dm should be true")
	}
	if policy.AllowGroups {
		t.Fatal("default allow_groups should be false")
	}
	if got := strings.Join(policy.AllowedSenders, ","); got != "U1,U2" {
		t.Fatalf("allowed senders = %q", got)
	}
	if got := strings.Join(policy.PairedGroups, ","); got != "G1" {
		t.Fatalf("paired groups = %q", got)
	}
}

func TestResolveChannelPolicyConfigDisabled(t *testing.T) {
	_, ok := ResolveChannelPolicyConfig(ChannelPolicyConfig{})
	if ok {
		t.Fatal("disabled channel policy should not be resolved")
	}
}

func TestLoadConfig_OllamaLegacyModelFieldsAreIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  chat_model: "Chat"
  worker_model: "Worker"

session:
  storage_dir: "./data/sessions"
`

	os.WriteFile(configPath, []byte(configContent), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Ollama.Model != "picoclaw-v1" {
		t.Errorf("Expected legacy ollama fields to be ignored and default model to be used, got '%s'", cfg.Ollama.Model)
	}
}

func TestLoadConfig_WithEnvVars(t *testing.T) {
	os.Setenv("ANTHROPIC_API_KEY", "test-anthropic-key")
	os.Setenv("DEEPSEEK_API_KEY", "test-deepseek-key")
	os.Setenv("OPENAI_API_KEY", "test-openai-key")
	defer func() {
		os.Unsetenv("ANTHROPIC_API_KEY")
		os.Unsetenv("DEEPSEEK_API_KEY")
		os.Unsetenv("OPENAI_API_KEY")
	}()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

claude:
  api_key: "${ANTHROPIC_API_KEY}"

deepseek:
  api_key: "${DEEPSEEK_API_KEY}"

openai:
  api_key: "${OPENAI_API_KEY}"

session:
  storage_dir: "./data/sessions"

log:
  level: "info"
`

	os.WriteFile(configPath, []byte(configContent), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Claude.APIKey != "test-anthropic-key" {
		t.Errorf("Expected Anthropic API key from env, got '%s'", cfg.Claude.APIKey)
	}

	if cfg.DeepSeek.APIKey != "test-deepseek-key" {
		t.Errorf("Expected DeepSeek API key from env, got '%s'", cfg.DeepSeek.APIKey)
	}

	if cfg.OpenAI.APIKey != "test-openai-key" {
		t.Errorf("Expected OpenAI API key from env, got '%s'", cfg.OpenAI.APIKey)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Expected error for non-existent config file")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	invalidContent := `
server:
  port: invalid_port
  host: "localhost"
invalid yaml content here
`

	os.WriteFile(configPath, []byte(invalidContent), 0644)

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Error("Expected error for invalid YAML")
	}
}

func TestLoadConfig_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "minimal.yaml")

	minimalContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"

session:
  storage_dir: "./data/sessions"
`

	os.WriteFile(configPath, []byte(minimalContent), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Ollamaモデルデフォルト
	if cfg.Ollama.Model != "picoclaw-v1" {
		t.Errorf("Expected Ollama model 'picoclaw-v1', got '%s'", cfg.Ollama.Model)
	}

	if cfg.Log.Level == "" {
		t.Error("Log level should have default value")
	}

	// Worker設定デフォルト値の確認
	if cfg.Worker.CommitMessagePrefix != "[Worker Auto-Commit]" {
		t.Errorf("Expected Worker CommitMessagePrefix '[Worker Auto-Commit]', got '%s'", cfg.Worker.CommitMessagePrefix)
	}

	if cfg.Worker.CommandTimeout != 300 {
		t.Errorf("Expected Worker CommandTimeout 300, got %d", cfg.Worker.CommandTimeout)
	}

	if cfg.Worker.GitTimeout != 30 {
		t.Errorf("Expected Worker GitTimeout 30, got %d", cfg.Worker.GitTimeout)
	}

	if len(cfg.Worker.ProtectedPatterns) != 4 {
		t.Errorf("Expected 4 protected patterns, got %d", len(cfg.Worker.ProtectedPatterns))
	}

	if cfg.Worker.ActionOnProtected != "error" {
		t.Errorf("Expected Worker ActionOnProtected 'error', got '%s'", cfg.Worker.ActionOnProtected)
	}

	if cfg.Worker.Workspace != "." {
		t.Errorf("Expected Worker Workspace '.', got '%s'", cfg.Worker.Workspace)
	}

	// v4デフォルト
	if cfg.Worker.MaxParallelism != 4 {
		t.Errorf("Expected Worker MaxParallelism 4, got %d", cfg.Worker.MaxParallelism)
	}

	// Distributed/IdleChat はデフォルトで無効
	if cfg.Distributed.Enabled {
		t.Error("Distributed should be disabled by default")
	}
	if cfg.IdleChat.Enabled {
		t.Error("IdleChat should be disabled by default")
	}

	if cfg.Security.PolicyMode != "balanced" {
		t.Errorf("Expected Security PolicyMode 'balanced', got '%s'", cfg.Security.PolicyMode)
	}
	if cfg.Security.NetworkScope != "" {
		t.Errorf("Expected Security NetworkScope '', got '%s'", cfg.Security.NetworkScope)
	}
	if cfg.Sandbox.Root != "sandbox" {
		t.Errorf("Expected Sandbox Root 'sandbox', got '%s'", cfg.Sandbox.Root)
	}
	if cfg.Sandbox.Storage != "jsonl" {
		t.Errorf("Expected Sandbox Storage jsonl, got '%s'", cfg.Sandbox.Storage)
	}
	if cfg.Sandbox.SQLitePath != cfg.WorkspaceDir+"/logs/sandbox.db" {
		t.Errorf("Expected Sandbox SQLitePath under workspace, got '%s'", cfg.Sandbox.SQLitePath)
	}
	if !cfg.Sandbox.Promotion.RequireDiff || !cfg.Sandbox.Promotion.RequireHumanApproval || !cfg.Sandbox.Promotion.RequireRollbackPlan {
		t.Errorf("Expected Sandbox promotion gate defaults to require diff, approval, and rollback: %+v", cfg.Sandbox.Promotion)
	}
	if !cfg.ToolHarness.IsEnabled() {
		t.Error("ToolHarness should be enabled by default")
	}
	if cfg.ToolHarness.Mode != "validate_then_repair" {
		t.Errorf("Expected ToolHarness Mode 'validate_then_repair', got '%s'", cfg.ToolHarness.Mode)
	}
	if !cfg.ToolHarness.ShouldRecordEvents() {
		t.Error("ToolHarness record_events should be enabled by default")
	}
	if cfg.ToolHarness.LogPath != cfg.WorkspaceDir+"/logs/tool_mediation.jsonl" {
		t.Errorf("Expected ToolHarness LogPath under workspace, got '%s'", cfg.ToolHarness.LogPath)
	}
	if !cfg.DCI.IsEnabled() {
		t.Error("DCI should be enabled by default")
	}
	if cfg.DCI.TracePath != cfg.WorkspaceDir+"/logs/dci_search_trace.jsonl" {
		t.Errorf("Expected DCI TracePath under workspace, got '%s'", cfg.DCI.TracePath)
	}
	if cfg.DCI.Storage != "jsonl" {
		t.Errorf("Expected DCI Storage 'jsonl', got '%s'", cfg.DCI.Storage)
	}
	if cfg.DCI.SQLitePath != cfg.WorkspaceDir+"/dci.db" {
		t.Errorf("Expected DCI SQLitePath under workspace, got '%s'", cfg.DCI.SQLitePath)
	}
	if cfg.DCI.MaxSeconds != 10 || cfg.DCI.MaxSteps != 8 || cfg.DCI.MaxCandidateFiles != 50 || cfg.DCI.MaxFilesRead != 10 || cfg.DCI.MaxEvidence != 6 || cfg.DCI.MaxSnippetChars != 800 {
		t.Errorf("unexpected DCI limits: %+v", cfg.DCI)
	}
	if len(cfg.DCI.KnowledgeFTSDomains) != 3 || cfg.DCI.KnowledgeFTSDomains[0] != "general" || cfg.DCI.KnowledgeFTSDomains[1] != "creative" || cfg.DCI.KnowledgeFTSDomains[2] != "news" {
		t.Errorf("unexpected DCI knowledge FTS domains: %+v", cfg.DCI.KnowledgeFTSDomains)
	}
	if !cfg.SkillGovernance.IsEnabled() {
		t.Error("SkillGovernance should be enabled by default")
	}
	if cfg.SkillGovernance.RegistryPath != cfg.WorkspaceDir+"/logs/skill_governance" {
		t.Errorf("Expected SkillGovernance RegistryPath under workspace, got '%s'", cfg.SkillGovernance.RegistryPath)
	}
	if cfg.SkillGovernance.Storage != "jsonl" {
		t.Errorf("Expected SkillGovernance Storage jsonl, got '%s'", cfg.SkillGovernance.Storage)
	}
	if cfg.SkillGovernance.SQLitePath != cfg.WorkspaceDir+"/logs/skill_governance.db" {
		t.Errorf("Expected SkillGovernance SQLitePath under workspace, got '%s'", cfg.SkillGovernance.SQLitePath)
	}
	if len(cfg.SkillGovernance.SkillRoots) != 3 {
		t.Errorf("Expected 3 SkillGovernance roots, got %+v", cfg.SkillGovernance.SkillRoots)
	}
	if !cfg.SkillGovernance.RequiredForCoder || !cfg.SkillGovernance.RequiredForWorker || !cfg.SkillGovernance.WarnIfSkillNotUsed {
		t.Errorf("unexpected SkillGovernance bootstrap defaults: %+v", cfg.SkillGovernance)
	}
	if !cfg.SkillGovernance.ContributionGate.Enabled || !cfg.SkillGovernance.ContributionGate.RequireHumanApproval {
		t.Errorf("unexpected SkillGovernance contribution defaults: %+v", cfg.SkillGovernance.ContributionGate)
	}
	if !cfg.Workstream.IsEnabled() {
		t.Error("Workstream should be enabled by default")
	}
	if cfg.Workstream.LogPath != cfg.WorkspaceDir+"/logs/workstream" {
		t.Errorf("Expected Workstream LogPath under workspace, got '%s'", cfg.Workstream.LogPath)
	}
	if cfg.Workstream.Storage != "jsonl" {
		t.Errorf("Expected Workstream Storage jsonl, got '%s'", cfg.Workstream.Storage)
	}
	if cfg.Workstream.SQLitePath != cfg.WorkspaceDir+"/logs/workstream.db" {
		t.Errorf("Expected Workstream SQLitePath under workspace, got '%s'", cfg.Workstream.SQLitePath)
	}
	if cfg.Workstream.VaultRoot != "vault/workstreams" {
		t.Errorf("Expected Workstream VaultRoot 'vault/workstreams', got '%s'", cfg.Workstream.VaultRoot)
	}
	if !cfg.Workstream.RequireSuccessCriteria || !cfg.Workstream.RequireVerification || !cfg.Workstream.DraftReportOnlyHeartbeat {
		t.Errorf("unexpected Workstream defaults: %+v", cfg.Workstream)
	}
	if !cfg.Revenue.IsEnabled() {
		t.Error("Revenue should be enabled by default")
	}
	if cfg.Revenue.LogPath != cfg.WorkspaceDir+"/logs/revenue" {
		t.Errorf("Expected Revenue LogPath under workspace, got '%s'", cfg.Revenue.LogPath)
	}
	if cfg.Revenue.Storage != "jsonl" {
		t.Errorf("Expected Revenue Storage jsonl, got '%s'", cfg.Revenue.Storage)
	}
	if cfg.Revenue.SQLitePath != cfg.WorkspaceDir+"/logs/revenue.db" {
		t.Errorf("Expected Revenue SQLitePath under workspace, got '%s'", cfg.Revenue.SQLitePath)
	}
	if !cfg.Revenue.ProhibitSuccessGuarantee || !cfg.Revenue.RequireCustomerVoicePermission || !cfg.Revenue.ExternalPublishRequiresApproval {
		t.Errorf("unexpected Revenue defaults: %+v", cfg.Revenue)
	}
	if !cfg.PersonaArchitecture.IsEnabled() {
		t.Error("PersonaArchitecture should be enabled by default")
	}
	if cfg.PersonaArchitecture.LogPath != cfg.WorkspaceDir+"/logs/persona" {
		t.Errorf("Expected PersonaArchitecture LogPath under workspace, got '%s'", cfg.PersonaArchitecture.LogPath)
	}
	if cfg.PersonaArchitecture.CharacterRoot != cfg.WorkspaceDir {
		t.Errorf("Expected PersonaArchitecture CharacterRoot workspace, got '%s'", cfg.PersonaArchitecture.CharacterRoot)
	}
	if cfg.PersonaArchitecture.Storage != "jsonl" {
		t.Errorf("Expected PersonaArchitecture Storage jsonl, got '%s'", cfg.PersonaArchitecture.Storage)
	}
	if cfg.PersonaArchitecture.SQLitePath != cfg.WorkspaceDir+"/logs/persona.db" {
		t.Errorf("Expected PersonaArchitecture SQLitePath under workspace, got '%s'", cfg.PersonaArchitecture.SQLitePath)
	}
	if !cfg.PersonaArchitecture.RequireLorePersonaSplit ||
		!cfg.PersonaArchitecture.RequireTriggerCategories ||
		!cfg.PersonaArchitecture.HumanReviewRequiredForMeta ||
		!cfg.PersonaArchitecture.RequireSessionKeying ||
		cfg.PersonaArchitecture.MaxTriggerCandidates != 15 {
		t.Errorf("unexpected PersonaArchitecture defaults: %+v", cfg.PersonaArchitecture)
	}
	if !cfg.BrowserTraceToAPI.IsEnabled() {
		t.Error("BrowserTraceToAPI should be enabled by default")
	}
	if cfg.BrowserTraceToAPI.LogPath != cfg.WorkspaceDir+"/logs/browser_trace_to_api" {
		t.Errorf("Expected BrowserTraceToAPI LogPath under workspace, got '%s'", cfg.BrowserTraceToAPI.LogPath)
	}
	if cfg.BrowserTraceToAPI.Storage != "jsonl" {
		t.Errorf("Expected BrowserTraceToAPI Storage 'jsonl', got '%s'", cfg.BrowserTraceToAPI.Storage)
	}
	if cfg.BrowserTraceToAPI.SQLitePath != cfg.WorkspaceDir+"/browser_trace_to_api.db" {
		t.Errorf("Expected BrowserTraceToAPI SQLitePath under workspace, got '%s'", cfg.BrowserTraceToAPI.SQLitePath)
	}
	if !cfg.BrowserTraceToAPI.ReadOnlyOnly ||
		!cfg.BrowserTraceToAPI.RequireTermsReview ||
		!cfg.BrowserTraceToAPI.RequireHumanApprovalPromote ||
		len(cfg.BrowserTraceToAPI.DenyMethods) != 3 {
		t.Errorf("unexpected BrowserTraceToAPI defaults: %+v", cfg.BrowserTraceToAPI)
	}
	if !cfg.ComplexityHotspot.IsEnabled() {
		t.Error("ComplexityHotspot should be enabled by default")
	}
	if cfg.ComplexityHotspot.LogPath != cfg.WorkspaceDir+"/logs/complexity_hotspot" {
		t.Errorf("Expected ComplexityHotspot LogPath under workspace, got '%s'", cfg.ComplexityHotspot.LogPath)
	}
	if cfg.ComplexityHotspot.Storage != "jsonl" {
		t.Errorf("Expected ComplexityHotspot Storage 'jsonl', got '%s'", cfg.ComplexityHotspot.Storage)
	}
	if cfg.ComplexityHotspot.SQLitePath != cfg.WorkspaceDir+"/logs/complexity_hotspot.db" {
		t.Errorf("Expected ComplexityHotspot SQLitePath under workspace, got '%s'", cfg.ComplexityHotspot.SQLitePath)
	}
	if cfg.ComplexityHotspot.DefaultMode != "report_only" ||
		cfg.ComplexityHotspot.MaxHotspots != 20 ||
		cfg.ComplexityHotspot.AutoApply ||
		!cfg.ComplexityHotspot.RequireHumanApprovalForPatch ||
		!cfg.ComplexityHotspot.OneHotspotPerPR {
		t.Errorf("unexpected ComplexityHotspot defaults: %+v", cfg.ComplexityHotspot)
	}
	if !cfg.SuperAgentHarness.IsEnabled() {
		t.Error("SuperAgentHarness should be enabled by default")
	}
	if cfg.SuperAgentHarness.LogPath != cfg.WorkspaceDir+"/logs/superagent_harness" {
		t.Errorf("Expected SuperAgentHarness LogPath under workspace, got '%s'", cfg.SuperAgentHarness.LogPath)
	}
	if cfg.SuperAgentHarness.Storage != "jsonl" {
		t.Errorf("Expected SuperAgentHarness Storage 'jsonl', got '%s'", cfg.SuperAgentHarness.Storage)
	}
	if cfg.SuperAgentHarness.SQLitePath != cfg.WorkspaceDir+"/logs/superagent_harness.db" {
		t.Errorf("Expected SuperAgentHarness SQLitePath under workspace, got '%s'", cfg.SuperAgentHarness.SQLitePath)
	}
	if cfg.SuperAgentHarness.MaxParallelSubagents != 4 ||
		cfg.SuperAgentHarness.MaxContextPackTokens != 3000 ||
		cfg.SuperAgentHarness.RunQueueSchedulerEnabled ||
		cfg.SuperAgentHarness.RunQueueSchedulerIntervalSec != 60 ||
		cfg.SuperAgentHarness.RunQueueSchedulerClaimLimit != 1 ||
		!cfg.SuperAgentHarness.RequireScope ||
		!cfg.SuperAgentHarness.RequireTerminationCondition ||
		!cfg.SuperAgentHarness.ReturnSummaryOnly ||
		!cfg.SuperAgentHarness.PromotionGateRequired ||
		!cfg.SuperAgentHarness.ExternalSendRequiresApproval ||
		!cfg.SuperAgentHarness.TraceAgentRun {
		t.Errorf("unexpected SuperAgentHarness defaults: %+v", cfg.SuperAgentHarness)
	}
	if !cfg.AIWorkflow.IsEnabled() {
		t.Error("AIWorkflow should be enabled by default")
	}
	if cfg.AIWorkflow.LogPath != cfg.WorkspaceDir+"/logs/ai_workflow" {
		t.Errorf("Expected AIWorkflow LogPath under workspace, got '%s'", cfg.AIWorkflow.LogPath)
	}
	if cfg.AIWorkflow.Storage != "jsonl" {
		t.Errorf("Expected AIWorkflow Storage 'jsonl', got '%s'", cfg.AIWorkflow.Storage)
	}
	if cfg.AIWorkflow.SQLitePath != cfg.WorkspaceDir+"/logs/ai_workflow.db" {
		t.Errorf("Expected AIWorkflow SQLitePath under workspace, got '%s'", cfg.AIWorkflow.SQLitePath)
	}
	if cfg.AIWorkflow.ProjectMemoryRoot != ".ai" {
		t.Errorf("Expected AIWorkflow ProjectMemoryRoot '.ai', got '%s'", cfg.AIWorkflow.ProjectMemoryRoot)
	}
	if cfg.AIWorkflow.WorktreeBaseDir != "../worktrees" {
		t.Errorf("Expected AIWorkflow WorktreeBaseDir '../worktrees', got '%s'", cfg.AIWorkflow.WorktreeBaseDir)
	}
	if len(cfg.AIWorkflow.RequiredCLITools) != 4 {
		t.Errorf("Expected 4 AIWorkflow required CLI tools, got %+v", cfg.AIWorkflow.RequiredCLITools)
	}
	if !cfg.AIWorkflow.RequiredBeforeModify ||
		!cfg.AIWorkflow.WorktreeRequiredForWrite ||
		!cfg.AIWorkflow.ContextTrackingEnabled {
		t.Errorf("unexpected AIWorkflow defaults: %+v", cfg.AIWorkflow)
	}
	if !cfg.KnowledgeMemory.IsEnabled() {
		t.Error("KnowledgeMemory should be enabled by default")
	}
	if cfg.KnowledgeMemory.LogPath != cfg.WorkspaceDir+"/logs/knowledge_memory" {
		t.Errorf("Expected KnowledgeMemory LogPath under workspace, got '%s'", cfg.KnowledgeMemory.LogPath)
	}
	if cfg.KnowledgeMemory.Storage != "jsonl" {
		t.Errorf("Expected KnowledgeMemory Storage jsonl, got '%s'", cfg.KnowledgeMemory.Storage)
	}
	if cfg.KnowledgeMemory.SQLitePath != cfg.WorkspaceDir+"/logs/knowledge_memory.db" {
		t.Errorf("Expected KnowledgeMemory SQLitePath under workspace, got '%s'", cfg.KnowledgeMemory.SQLitePath)
	}
	if !cfg.KnowledgeMemory.ProtectPersonalArchive ||
		!cfg.KnowledgeMemory.DreamRequiresHumanReview ||
		!cfg.KnowledgeMemory.DailyIntakePromoteToStaging {
		t.Errorf("unexpected KnowledgeMemory defaults: %+v", cfg.KnowledgeMemory)
	}
	if cfg.AudioRouter.ConnectTimeoutMS != 5000 {
		t.Errorf("Expected AudioRouter ConnectTimeoutMS 5000, got %d", cfg.AudioRouter.ConnectTimeoutMS)
	}
	if cfg.AudioRouter.BufferMS != 120 {
		t.Errorf("Expected AudioRouter BufferMS 120, got %d", cfg.AudioRouter.BufferMS)
	}
}

func TestLoadConfig_ToolHarnessOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tool_harness.yaml")

	content := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"

session:
  storage_dir: "./data/sessions"

workspace_dir: "./workspace"

tool_harness:
  enabled: false
  mode: strict
  record_events: false
  log_path: "./logs/custom_tool_mediation.jsonl"
`

	os.WriteFile(configPath, []byte(content), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.ToolHarness.IsEnabled() {
		t.Fatal("ToolHarness should be disabled by explicit config")
	}
	if cfg.ToolHarness.ShouldRecordEvents() {
		t.Fatal("ToolHarness record_events should be disabled by explicit config")
	}
	if cfg.ToolHarness.Mode != "strict" {
		t.Fatalf("unexpected ToolHarness Mode: %s", cfg.ToolHarness.Mode)
	}
	if cfg.ToolHarness.LogPath != "./logs/custom_tool_mediation.jsonl" {
		t.Fatalf("unexpected ToolHarness LogPath: %s", cfg.ToolHarness.LogPath)
	}
}

func TestLoadConfig_ToolHarnessInvalidMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tool_harness_invalid.yaml")

	content := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"

session:
  storage_dir: "./data/sessions"

tool_harness:
  mode: unsafe
`

	os.WriteFile(configPath, []byte(content), 0644)

	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("expected invalid tool_harness.mode error")
	}
}

func TestLoadConfig_DCIOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "dci.yaml")

	content := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"

session:
  storage_dir: "./data/sessions"

dci:
  enabled: false
  storage: sqlite
  trace_path: "./logs/custom_dci.jsonl"
  sqlite_path: "./data/custom_dci.db"
  corpus_allowlist:
    - "docs/10_新仕様"
  corpus_denylist:
    - ".env"
  knowledge_fts_domains:
    - "general"
    - "movie"
  explicit_keywords:
    - "原文確認"
  max_seconds: 9
  max_steps: 7
  max_candidate_files: 11
  max_files_read: 4
  max_evidence: 3
  max_snippet_chars: 120
`

	os.WriteFile(configPath, []byte(content), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.DCI.IsEnabled() {
		t.Fatal("DCI should be disabled by explicit config")
	}
	if cfg.DCI.TracePath != "./logs/custom_dci.jsonl" {
		t.Fatalf("unexpected DCI TracePath: %s", cfg.DCI.TracePath)
	}
	if cfg.DCI.Storage != "sqlite" {
		t.Fatalf("unexpected DCI Storage: %s", cfg.DCI.Storage)
	}
	if cfg.DCI.SQLitePath != "./data/custom_dci.db" {
		t.Fatalf("unexpected DCI SQLitePath: %s", cfg.DCI.SQLitePath)
	}
	if cfg.DCI.MaxSeconds != 9 || cfg.DCI.MaxSteps != 7 || cfg.DCI.MaxCandidateFiles != 11 || cfg.DCI.MaxFilesRead != 4 || cfg.DCI.MaxEvidence != 3 || cfg.DCI.MaxSnippetChars != 120 {
		t.Fatalf("unexpected DCI limits: %+v", cfg.DCI)
	}
	if len(cfg.DCI.KnowledgeFTSDomains) != 2 || cfg.DCI.KnowledgeFTSDomains[0] != "general" || cfg.DCI.KnowledgeFTSDomains[1] != "movie" {
		t.Fatalf("unexpected DCI knowledge FTS domains: %+v", cfg.DCI.KnowledgeFTSDomains)
	}
}

func TestLoadConfig_DCIInvalidStorage(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "dci_invalid_storage.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
dci:
  storage: memory
`

	os.WriteFile(configPath, []byte(content), 0644)

	if _, err := LoadConfig(configPath); err == nil {
		t.Fatal("expected invalid dci.storage error")
	}
}

func TestLoadConfig_ConversationL1SQLitePath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "conversation_l1.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
conversation:
  enabled: true
  redis_url: "redis://localhost:6379"
  l1_sqlite_path: "./data/l1_memory.db"
  duckdb_path: "./data/memory.duckdb"
  vectordb_url: "localhost:6334"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Conversation.L1SQLitePath != "./data/l1_memory.db" {
		t.Fatalf("unexpected l1 sqlite path: %s", cfg.Conversation.L1SQLitePath)
	}
}

func TestLoadConfig_OperationMemoryDirDefault(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "operation_memory_default.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".picoclaw", "rencrow", "memory")
	if cfg.OperationMemoryDir != want {
		t.Fatalf("unexpected operation memory dir: got %q want %q", cfg.OperationMemoryDir, want)
	}
}

func TestLoadConfig_OperationMemoryDirOverride(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "operation_memory_override.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
operation_memory_dir: "/tmp/rencrow-operation-memory"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.OperationMemoryDir != "/tmp/rencrow-operation-memory" {
		t.Fatalf("unexpected operation memory dir: %s", cfg.OperationMemoryDir)
	}
}

func TestLoadConfig_LocalLLMDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "local_llm.yaml")

	content := `
server:
  port: 8080
session:
  storage_dir: "./data/sessions"
local_llm:
  enabled: true
  base_url: "http://127.0.0.1:8080"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.LocalLLM.Enabled {
		t.Fatal("expected local_llm enabled")
	}
	if cfg.LocalLLM.Provider != "local_openai" {
		t.Fatalf("unexpected local_llm provider: %s", cfg.LocalLLM.Provider)
	}
	if cfg.LocalLLM.ChatModel != "Chat" || cfg.LocalLLM.WorkerModel != "Worker" || cfg.LocalLLM.ChatWorkerModel != "ChatWorker" || cfg.LocalLLM.HeavyModel != "Heavy" || cfg.LocalLLM.WildModel != "Wild" {
		t.Fatalf("unexpected model aliases: %+v", cfg.LocalLLM)
	}
	if cfg.LocalLLM.TimeoutSec != 120 {
		t.Fatalf("unexpected timeout_sec: %d", cfg.LocalLLM.TimeoutSec)
	}
	if cfg.LocalLLM.GlobalConcurrency != 2 || cfg.LocalLLM.ModelConcurrency != 1 {
		t.Fatalf("unexpected concurrency defaults: %+v", cfg.LocalLLM)
	}
	if !cfg.LocalLLMWarmupEnabled() {
		t.Fatal("expected local_llm warmup enabled by default")
	}
}

func TestLoadConfig_WebwrightFetchDefaultsFromLocalWorker(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "webwright_fetch.yaml")

	content := `
server:
  port: 8080
session:
  storage_dir: "./data/sessions"
local_llm:
  enabled: true
  worker_base_url: "http://192.168.1.207:8082"
webwright_fetch:
  enabled: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.WebwrightFetch.Enabled {
		t.Fatal("expected webwright_fetch enabled")
	}
	wantToolsRoot := filepath.Join(userHomeDirForTest(t), "RenCrow", "RenCrow_Tools")
	if cfg.WebwrightFetch.RunnerPath != filepath.Join(wantToolsRoot, "tools", "webwright_fetch", "run_webwright_fetch.py") {
		t.Fatalf("unexpected runner path: %s", cfg.WebwrightFetch.RunnerPath)
	}
	if cfg.WebwrightFetch.ConfigPath != filepath.Join(wantToolsRoot, "tools", "webwright_fetch", "config_local_worker.yaml") {
		t.Fatalf("unexpected config path: %s", cfg.WebwrightFetch.ConfigPath)
	}
	if cfg.WebwrightFetch.ResponsesEndpoint != "http://192.168.1.207:8082/v1/responses" {
		t.Fatalf("unexpected responses endpoint: %s", cfg.WebwrightFetch.ResponsesEndpoint)
	}
	if cfg.WebwrightFetch.UvxFrom != "" {
		t.Fatalf("uvx_from must be opt-in, got %s", cfg.WebwrightFetch.UvxFrom)
	}
	if cfg.WebwrightFetch.Model != "Coder1" || cfg.WebwrightFetch.APIKey != "dummy" {
		t.Fatalf("unexpected local webwright model/key defaults: %+v", cfg.WebwrightFetch)
	}
}

func TestLoadConfig_WebwrightFetchKeepsExplicitUvxFrom(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "webwright_fetch_uvx.yaml")

	content := `
server:
  port: 8080
session:
  storage_dir: "./data/sessions"
local_llm:
  enabled: true
  worker_base_url: "http://192.168.1.207:8082"
webwright_fetch:
  enabled: true
  uvx_from: "git+https://github.com/microsoft/Webwright.git"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.WebwrightFetch.UvxFrom != "git+https://github.com/microsoft/Webwright.git" {
		t.Fatalf("explicit uvx_from should be preserved, got %s", cfg.WebwrightFetch.UvxFrom)
	}
}

func TestLoadConfig_WebGatherSearXNGBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "web_gather.yaml")

	content := `
server:
  port: 8080
session:
  storage_dir: "./data/sessions"
local_llm:
  enabled: true
  provider: local_openai
  base_url: "http://127.0.0.1:8081"
  chat_model: Chat
  worker_model: Worker
  heavy_model: Heavy
  wild_model: Wild
  timeout_sec: 30
web_gather:
  searxng_base_url: "http://127.0.0.1:8888"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.WebGather.SearXNGBaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("unexpected web gather config: %+v", cfg.WebGather)
	}
}

func TestLoadConfig_WebGatherRejectsInvalidSearXNGBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "web_gather_invalid.yaml")

	content := `
server:
  port: 8080
session:
  storage_dir: "./data/sessions"
local_llm:
  enabled: true
  provider: local_openai
  base_url: "http://127.0.0.1:8081"
  chat_model: Chat
  worker_model: Worker
  heavy_model: Heavy
  wild_model: Wild
  timeout_sec: 30
web_gather:
  searxng_base_url: "127.0.0.1:8888"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "web_gather.searxng_base_url") {
		t.Fatalf("expected web gather validation error, got %v", err)
	}
}

func TestLoadConfig_AudioRouterValidation(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "audio_router.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
audio_router:
  enabled: true
  sse_url: "http://127.0.0.1:18790/audio-router/events"
  device_map:
    mio:
      device_id: "{mio-device}"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.AudioRouter.DeviceMap["mio"].DeviceID != "{mio-device}" {
		t.Fatalf("unexpected mio device_id: %q", cfg.AudioRouter.DeviceMap["mio"].DeviceID)
	}
}

func TestLoadConfig_SecurityNetworkSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "network.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
security:
  enabled: true
  policy_mode: "strict"
  network_scope: "allowlist"
  network_allowlist:
    - "api.openai.com"
  audit:
    backend: "jsonl"
    path: "logs/execution_audit.jsonl"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Security.NetworkScope != "allowlist" {
		t.Fatalf("expected network_scope allowlist, got %s", cfg.Security.NetworkScope)
	}
	if len(cfg.Security.NetworkAllowlist) != 1 || cfg.Security.NetworkAllowlist[0] != "api.openai.com" {
		t.Fatalf("unexpected network_allowlist: %+v", cfg.Security.NetworkAllowlist)
	}
}

func TestLoadConfig_SandboxSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "sandbox.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
security:
  enabled: true
sandbox:
  enabled: true
  storage: "sqlite"
  root: "sandbox/workstreams"
  sqlite_path: "./logs/sandbox.db"
  deny_outside_sandbox_write: true
  promotion:
    require_diff: true
    require_reason: true
    require_test_result: true
    require_rollback_plan: true
    require_human_approval: true
    require_post_apply_verification: true
    apply_root: "../worktrees/rencrow-feature"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Sandbox.Enabled {
		t.Fatal("expected sandbox enabled")
	}
	if cfg.Sandbox.Root != "sandbox/workstreams" {
		t.Fatalf("sandbox root = %s", cfg.Sandbox.Root)
	}
	if cfg.Sandbox.Storage != "sqlite" || cfg.Sandbox.SQLitePath != "./logs/sandbox.db" {
		t.Fatalf("unexpected sandbox storage config: %+v", cfg.Sandbox)
	}
	if !cfg.Sandbox.DenyOutsideSandboxWrite {
		t.Fatal("expected deny_outside_sandbox_write")
	}
	if !cfg.Sandbox.Promotion.RequirePostApplyVerification {
		t.Fatalf("unexpected promotion config: %+v", cfg.Sandbox.Promotion)
	}
	if cfg.Sandbox.Promotion.ApplyRoot != "../worktrees/rencrow-feature" {
		t.Fatalf("promotion apply root = %s", cfg.Sandbox.Promotion.ApplyRoot)
	}
}

func TestLoadConfig_SkillGovernanceSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "skill_governance.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
skill_governance:
  enabled: true
  storage: "sqlite"
  registry_path: "./logs/skills"
  sqlite_path: "./logs/skills.db"
  skill_roots:
    - "skills"
    - "workspace/skills"
  required_for_coder: true
  required_for_worker: true
  warn_if_skill_not_used: true
  contribution_gate:
    enabled: true
    require_open_closed_pr_search: true
    require_real_problem: true
    require_complete_diff_review: true
    require_human_approval: true
    one_problem_per_pr: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.SkillGovernance.IsEnabled() {
		t.Fatal("expected skill governance enabled")
	}
	if cfg.SkillGovernance.Storage != "sqlite" || cfg.SkillGovernance.RegistryPath != "./logs/skills" || cfg.SkillGovernance.SQLitePath != "./logs/skills.db" {
		t.Fatalf("unexpected skill governance config: %+v", cfg.SkillGovernance)
	}
	if len(cfg.SkillGovernance.SkillRoots) != 2 || cfg.SkillGovernance.SkillRoots[1] != "workspace/skills" {
		t.Fatalf("skill roots = %+v", cfg.SkillGovernance.SkillRoots)
	}
	if !cfg.SkillGovernance.ContributionGate.OneProblemPerPR {
		t.Fatalf("unexpected contribution gate: %+v", cfg.SkillGovernance.ContributionGate)
	}
}

func TestConfigValidation_SkillGovernanceRequiresRegistryAndRoots(t *testing.T) {
	enabled := true
	cfg := &Config{
		SkillGovernance: SkillGovernanceConfig{
			Enabled:      &enabled,
			Storage:      "jsonl",
			RegistryPath: "",
			SkillRoots:   nil,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing skill governance registry to fail")
	}
	cfg.SkillGovernance.RegistryPath = "logs/skills"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing skill roots to fail")
	}
}

func TestConfigValidation_SkillGovernanceRejectsInvalidStorage(t *testing.T) {
	enabled := true
	cfg := &Config{
		SkillGovernance: SkillGovernanceConfig{
			Enabled:      &enabled,
			Storage:      "memory",
			RegistryPath: "logs/skills",
			SkillRoots:   []string{"skills"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid skill_governance.storage to fail")
	}
}

func TestLoadConfig_WorkstreamSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "workstream.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
workstream:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/workstream"
  sqlite_path: "./logs/workstream.db"
  vault_root: "./vault/workstreams"
  require_success_criteria: true
  require_verification: true
  draft_report_only_heartbeat: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Workstream.IsEnabled() {
		t.Fatal("expected workstream enabled")
	}
	if cfg.Workstream.Storage != "sqlite" || cfg.Workstream.LogPath != "./logs/workstream" || cfg.Workstream.SQLitePath != "./logs/workstream.db" || cfg.Workstream.VaultRoot != "./vault/workstreams" {
		t.Fatalf("unexpected workstream config: %+v", cfg.Workstream)
	}
}

func TestConfigValidation_WorkstreamRequiresLogAndVault(t *testing.T) {
	enabled := true
	cfg := &Config{
		Workstream: WorkstreamConfig{
			Enabled:   &enabled,
			Storage:   "jsonl",
			LogPath:   "",
			VaultRoot: "",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing workstream log path to fail")
	}
	cfg.Workstream.LogPath = "logs/workstream"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing workstream vault root to fail")
	}
}

func TestConfigValidation_WorkstreamRejectsInvalidStorage(t *testing.T) {
	enabled := true
	cfg := &Config{
		Workstream: WorkstreamConfig{
			Enabled:   &enabled,
			Storage:   "memory",
			LogPath:   "logs/workstream",
			VaultRoot: "vault/workstreams",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid workstream.storage to fail")
	}
}

func TestLoadConfig_RevenueSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "revenue.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
revenue:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/revenue"
  sqlite_path: "./logs/revenue.db"
  prohibit_success_guarantee: true
  require_customer_voice_permission: true
  external_publish_requires_approval: true
  high_ticket_offer_requires_approval: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Revenue.IsEnabled() {
		t.Fatal("expected revenue enabled")
	}
	if cfg.Revenue.Storage != "sqlite" || cfg.Revenue.LogPath != "./logs/revenue" || cfg.Revenue.SQLitePath != "./logs/revenue.db" {
		t.Fatalf("unexpected revenue config: %+v", cfg.Revenue)
	}
	if !cfg.Revenue.RequireCustomerVoicePermission || !cfg.Revenue.HighTicketOfferRequiresApproval {
		t.Fatalf("unexpected revenue gates: %+v", cfg.Revenue)
	}
}

func TestConfigValidation_RevenueRequiresLogPath(t *testing.T) {
	enabled := true
	cfg := &Config{
		Revenue: RevenueConfig{
			Enabled: &enabled,
			Storage: "jsonl",
			LogPath: "",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing revenue log path to fail")
	}
}

func TestConfigValidation_RevenueRejectsInvalidStorage(t *testing.T) {
	enabled := true
	cfg := &Config{
		Revenue: RevenueConfig{
			Enabled: &enabled,
			Storage: "memory",
			LogPath: "logs/revenue",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid revenue.storage to fail")
	}
}

func TestLoadConfig_PersonaArchitectureSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "persona.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
persona_architecture:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/persona"
  sqlite_path: "./logs/persona.db"
  character_root: "./persona"
  trigger_category_path: "persona_triggers"
  canonical_response_path: "persona_canonicals"
  canonical_response_cooldown_turns: 7
  canonical_response_max_per_session: 2
  require_lore_persona_split: true
  require_trigger_categories: true
  human_review_required_for_meta: true
  require_session_keying: true
  max_trigger_candidates: 12
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.PersonaArchitecture.IsEnabled() {
		t.Fatal("expected persona architecture enabled")
	}
	if cfg.PersonaArchitecture.Storage != "sqlite" ||
		cfg.PersonaArchitecture.LogPath != "./logs/persona" ||
		cfg.PersonaArchitecture.SQLitePath != "./logs/persona.db" ||
		cfg.PersonaArchitecture.CharacterRoot != "./persona" ||
		cfg.PersonaArchitecture.TriggerCategoryPath != "persona_triggers" ||
		cfg.PersonaArchitecture.CanonicalResponsePath != "persona_canonicals" ||
		cfg.PersonaArchitecture.CanonicalResponseCooldownTurns != 7 ||
		cfg.PersonaArchitecture.CanonicalResponseMaxPerSession != 2 ||
		cfg.PersonaArchitecture.MaxTriggerCandidates != 12 {
		t.Fatalf("unexpected persona architecture config: %+v", cfg.PersonaArchitecture)
	}
}

func TestConfigValidation_PersonaArchitectureRequiresLogPath(t *testing.T) {
	enabled := true
	cfg := &Config{
		PersonaArchitecture: PersonaArchitectureConfig{
			Enabled: &enabled,
			Storage: "jsonl",
			LogPath: "",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing persona_architecture log path to fail")
	}
}

func TestConfigValidation_PersonaArchitectureRejectsInvalidStorage(t *testing.T) {
	enabled := true
	cfg := &Config{
		PersonaArchitecture: PersonaArchitectureConfig{
			Enabled: &enabled,
			Storage: "memory",
			LogPath: "logs/persona",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid persona_architecture.storage to fail")
	}
}

func TestConfigValidation_PersonaArchitectureRejectsInvalidCanonicalPolicy(t *testing.T) {
	enabled := true
	cfg := &Config{
		PersonaArchitecture: PersonaArchitectureConfig{
			Enabled:                        &enabled,
			Storage:                        "jsonl",
			LogPath:                        "logs/persona",
			CharacterRoot:                  "workspace",
			CanonicalResponseCooldownTurns: -1,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid persona_architecture.canonical_response_cooldown_turns to fail")
	}

	cfg.PersonaArchitecture.CanonicalResponseCooldownTurns = 0
	cfg.PersonaArchitecture.CanonicalResponseMaxPerSession = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid persona_architecture.canonical_response_max_per_session to fail")
	}
}

func TestLoadConfig_BrowserTraceToAPISettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "browser_trace.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
browser_trace_to_api:
  enabled: true
  storage: sqlite
  log_path: "./logs/browser_trace_to_api"
  sqlite_path: "./data/browser_trace_to_api.db"
  read_only_only: true
  require_terms_review: true
  require_human_approval_for_promote: true
  generate_openapi: true
  generate_coverage_report: true
  accepted_paths:
    - ".o11y/"
    - "traces/"
  deny_methods:
    - PUT
    - PATCH
    - DELETE
  deny_sensitive_flows:
    - payment
    - purchase
    - refund
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.BrowserTraceToAPI.IsEnabled() {
		t.Fatal("expected browser trace to api enabled")
	}
	if cfg.BrowserTraceToAPI.LogPath != "./logs/browser_trace_to_api" ||
		cfg.BrowserTraceToAPI.Storage != "sqlite" ||
		cfg.BrowserTraceToAPI.SQLitePath != "./data/browser_trace_to_api.db" ||
		len(cfg.BrowserTraceToAPI.AcceptedPaths) != 2 ||
		len(cfg.BrowserTraceToAPI.DenyMethods) != 3 ||
		len(cfg.BrowserTraceToAPI.DenySensitiveFlows) != 3 {
		t.Fatalf("unexpected browser trace config: %+v", cfg.BrowserTraceToAPI)
	}
}

func TestConfigValidation_BrowserTraceToAPIRejectsSafeDenyMethod(t *testing.T) {
	cfg := &Config{
		BrowserTraceToAPI: BrowserTraceToAPIConfig{
			LogPath:     "logs/browser_trace_to_api",
			DenyMethods: []string{"GET"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected safe deny method to fail")
	}
}

func TestConfigValidation_BrowserTraceToAPIRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		BrowserTraceToAPI: BrowserTraceToAPIConfig{
			Storage: "bad",
			LogPath: "logs/browser_trace_to_api",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid browser trace storage to fail")
	}
}

func TestConfigValidation_BrowserTraceToAPIRejectsUnsafeAcceptedPath(t *testing.T) {
	cfg := &Config{
		BrowserTraceToAPI: BrowserTraceToAPIConfig{
			LogPath:       "logs/browser_trace_to_api",
			AcceptedPaths: []string{"../secrets"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsafe accepted trace path to fail")
	}
}

func TestConfigValidation_BrowserTraceToAPIRejectsEmptySensitiveFlow(t *testing.T) {
	cfg := &Config{
		BrowserTraceToAPI: BrowserTraceToAPIConfig{
			LogPath:            "logs/browser_trace_to_api",
			DenySensitiveFlows: []string{""},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected empty sensitive flow to fail")
	}
}

func TestLoadConfig_ComplexityHotspotSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "complexity.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
complexity_hotspot:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/complexity"
  sqlite_path: "./data/complexity.db"
  default_mode: "report_only"
  max_hotspots: 12
  exclude_dirs:
    - node_modules
    - dist
  auto_apply: false
  require_human_approval_for_patch: true
  one_hotspot_per_pr: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.ComplexityHotspot.IsEnabled() {
		t.Fatal("expected complexity hotspot enabled")
	}
	if cfg.ComplexityHotspot.Storage != "sqlite" ||
		cfg.ComplexityHotspot.LogPath != "./logs/complexity" ||
		cfg.ComplexityHotspot.SQLitePath != "./data/complexity.db" ||
		cfg.ComplexityHotspot.MaxHotspots != 12 {
		t.Fatalf("unexpected complexity config: %+v", cfg.ComplexityHotspot)
	}
}

func TestConfigValidation_ComplexityHotspotRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		ComplexityHotspot: ComplexityHotspotConfig{
			Storage:     "bad",
			LogPath:     "logs/complexity",
			DefaultMode: "report_only",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid complexity hotspot storage error")
	}
}

func TestConfigValidation_ComplexityHotspotRejectsAutoApply(t *testing.T) {
	cfg := &Config{
		ComplexityHotspot: ComplexityHotspotConfig{
			LogPath:     "logs/complexity",
			DefaultMode: "report_only",
			AutoApply:   true,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected auto_apply to fail")
	}
}

func TestLoadConfig_SuperAgentHarnessSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "superagent.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
superagent_harness:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/superagent"
  sqlite_path: "./data/superagent.db"
  max_parallel_subagents: 3
  max_context_pack_tokens: 2500
  run_queue_scheduler_enabled: true
  run_queue_scheduler_interval_sec: 30
  run_queue_scheduler_claim_limit: 2
  require_scope: true
  require_termination_condition: true
  return_summary_only: true
  promotion_gate_required: true
  external_send_requires_approval: true
  trace_agent_run: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.SuperAgentHarness.IsEnabled() {
		t.Fatal("expected superagent enabled")
	}
	if cfg.SuperAgentHarness.Storage != "sqlite" ||
		cfg.SuperAgentHarness.LogPath != "./logs/superagent" ||
		cfg.SuperAgentHarness.SQLitePath != "./data/superagent.db" ||
		cfg.SuperAgentHarness.MaxContextPackTokens != 2500 ||
		!cfg.SuperAgentHarness.RunQueueSchedulerEnabled ||
		cfg.SuperAgentHarness.RunQueueSchedulerIntervalSec != 30 ||
		cfg.SuperAgentHarness.RunQueueSchedulerClaimLimit != 2 {
		t.Fatalf("unexpected superagent config: %+v", cfg.SuperAgentHarness)
	}
}

func TestConfigValidation_SuperAgentHarnessRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		SuperAgentHarness: SuperAgentHarnessConfig{
			Storage:              "bad",
			LogPath:              "logs/superagent",
			ReturnSummaryOnly:    true,
			TraceAgentRun:        true,
			MaxContextPackTokens: 3000,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid superagent storage error")
	}
}

func TestConfigValidation_SuperAgentHarnessRequiresTrace(t *testing.T) {
	cfg := &Config{
		SuperAgentHarness: SuperAgentHarnessConfig{
			LogPath:              "logs/superagent",
			ReturnSummaryOnly:    true,
			MaxContextPackTokens: 3000,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing trace_agent_run to fail")
	}
}

func TestLoadConfig_AIWorkflowSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "ai_workflow.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
ai_workflow:
  enabled: true
  storage: "sqlite"
  log_path: "./logs/ai_workflow"
  sqlite_path: "./data/ai_workflow.db"
  project_memory_root: ".project-ai"
  worktree_base_dir: "../ai-worktrees"
  required_before_modify: true
  worktree_required_for_write: true
  required_cli_tools:
    - "rg"
    - "git"
  context_tracking_enabled: true
  context_budget_tokens: 12000
  context_budget_warn_ratio: 0.7
  context_budget_stop_ratio: 0.9
  heavy_worker_enabled: true
  heavy_worker_require_reason: true
  heavy_worker_file_threshold: 25
  heavy_worker_spec_threshold: 2
  heavy_worker_retry_threshold: 3
  external_control_allowed_actors:
    - "Worker"
  external_control_allowed_channels:
    - "viewer"
  external_control_allowed_actions:
    - "promotion_apply"
  external_control_approval_required:
    - "promotion_apply"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.AIWorkflow.IsEnabled() {
		t.Fatal("expected ai workflow enabled")
	}
	if cfg.AIWorkflow.Storage != "sqlite" ||
		cfg.AIWorkflow.LogPath != "./logs/ai_workflow" ||
		cfg.AIWorkflow.SQLitePath != "./data/ai_workflow.db" ||
		cfg.AIWorkflow.ProjectMemoryRoot != ".project-ai" ||
		cfg.AIWorkflow.WorktreeBaseDir != "../ai-worktrees" {
		t.Fatalf("unexpected ai workflow config: %+v", cfg.AIWorkflow)
	}
	if len(cfg.AIWorkflow.RequiredCLITools) != 2 || cfg.AIWorkflow.RequiredCLITools[1] != "git" {
		t.Fatalf("unexpected required cli tools: %+v", cfg.AIWorkflow.RequiredCLITools)
	}
	if !cfg.AIWorkflow.RequiredBeforeModify || !cfg.AIWorkflow.WorktreeRequiredForWrite || !cfg.AIWorkflow.ContextTrackingEnabled {
		t.Fatalf("unexpected ai workflow gates: %+v", cfg.AIWorkflow)
	}
	if cfg.AIWorkflow.ContextBudgetTokens != 12000 ||
		cfg.AIWorkflow.ContextBudgetWarnRatio != 0.7 ||
		cfg.AIWorkflow.ContextBudgetStopRatio != 0.9 {
		t.Fatalf("unexpected ai workflow context budget config: %+v", cfg.AIWorkflow)
	}
	if !cfg.AIWorkflow.HeavyWorkerEnabled ||
		!cfg.AIWorkflow.HeavyWorkerRequireReason ||
		cfg.AIWorkflow.HeavyWorkerFileThreshold != 25 ||
		cfg.AIWorkflow.HeavyWorkerSpecThreshold != 2 ||
		cfg.AIWorkflow.HeavyWorkerRetryThreshold != 3 {
		t.Fatalf("unexpected ai workflow heavy worker config: %+v", cfg.AIWorkflow)
	}
	if len(cfg.AIWorkflow.ExternalControlAllowedActors) != 1 || cfg.AIWorkflow.ExternalControlAllowedActors[0] != "Worker" ||
		len(cfg.AIWorkflow.ExternalControlAllowedChannels) != 1 || cfg.AIWorkflow.ExternalControlAllowedChannels[0] != "viewer" ||
		len(cfg.AIWorkflow.ExternalControlAllowedActions) != 1 || cfg.AIWorkflow.ExternalControlAllowedActions[0] != "promotion_apply" ||
		len(cfg.AIWorkflow.ExternalControlApprovalRequired) != 1 || cfg.AIWorkflow.ExternalControlApprovalRequired[0] != "promotion_apply" {
		t.Fatalf("unexpected ai workflow external control config: %+v", cfg.AIWorkflow)
	}
}

func TestConfigValidation_AIWorkflowRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		AIWorkflow: AIWorkflowConfig{
			Storage:           "memory",
			LogPath:           "logs/ai_workflow",
			ProjectMemoryRoot: ".ai",
			WorktreeBaseDir:   "../worktrees",
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid ai_workflow.storage to fail")
	}
}

func TestConfigValidation_AIWorkflowRejectsInvalidContextBudget(t *testing.T) {
	cfg := &Config{
		AIWorkflow: AIWorkflowConfig{
			Storage:                "jsonl",
			LogPath:                "logs/ai_workflow",
			ProjectMemoryRoot:      ".ai",
			WorktreeBaseDir:        "../worktrees",
			ContextBudgetTokens:    1000,
			ContextBudgetWarnRatio: 0.95,
			ContextBudgetStopRatio: 0.8,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid ai_workflow context budget to fail")
	}
}

func TestConfigValidation_AIWorkflowRejectsInvalidHeavyWorkerPolicy(t *testing.T) {
	cfg := &Config{
		AIWorkflow: AIWorkflowConfig{
			Storage:                  "jsonl",
			LogPath:                  "logs/ai_workflow",
			ProjectMemoryRoot:        ".ai",
			WorktreeBaseDir:          "../worktrees",
			HeavyWorkerFileThreshold: -1,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid ai_workflow heavy worker policy to fail")
	}
}

func TestConfigValidation_AIWorkflowRejectsEmptyExternalControlPolicyValue(t *testing.T) {
	cfg := &Config{
		AIWorkflow: AIWorkflowConfig{
			Storage:                       "jsonl",
			LogPath:                       "logs/ai_workflow",
			ProjectMemoryRoot:             ".ai",
			WorktreeBaseDir:               "../worktrees",
			ExternalControlAllowedActions: []string{""},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid ai_workflow external control policy to fail")
	}
}

func TestConfigValidation_KnowledgeMemoryRequiresProtectedArchive(t *testing.T) {
	cfg := &Config{
		KnowledgeMemory: KnowledgeMemoryConfig{
			Storage:                  "jsonl",
			LogPath:                  "logs/knowledge_memory",
			DreamRequiresHumanReview: true,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected protect_personal_archive to fail")
	}
}

func TestConfigValidation_KnowledgeMemoryRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		KnowledgeMemory: KnowledgeMemoryConfig{
			Storage:                  "memory",
			LogPath:                  "logs/knowledge_memory",
			ProtectPersonalArchive:   true,
			DreamRequiresHumanReview: true,
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid knowledge_memory.storage to fail")
	}
}

func TestConfigValidation_SandboxWriteGateRequiresSecurity(t *testing.T) {
	cfg := &Config{
		Sandbox: SandboxConfig{
			Enabled:                 true,
			Storage:                 "jsonl",
			Root:                    "sandbox",
			DenyOutsideSandboxWrite: true,
		},
		Security: SecurityConfig{
			Enabled: false,
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected sandbox write gate to require security.enabled")
	}
}

func TestConfigValidation_SandboxRejectsInvalidStorage(t *testing.T) {
	cfg := &Config{
		Sandbox: SandboxConfig{
			Enabled: true,
			Storage: "memory",
			Root:    "sandbox",
		},
		Security: SecurityConfig{
			Enabled: true,
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid sandbox.storage to fail")
	}
}

func TestLoadConfig_TTSSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tts.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
tts:
  enabled: true
  output_dir: "./workspace/tts"
  http_base_url: "https://127.0.0.1:8770"
  tls_skip_verify: true
  timeout_ms: 15000
  voice_id: "mio"
  provider_params:
    style: "Neutral"
    style_weight: 2.8
  provider_priority: ["irodori", "sbv2", "azure", "eleven"]
  playback_commands:
    - name: "ffplay"
      args: ["-autoexit", "{audio}"]
  irodori:
    enabled: true
    base_url: "http://127.0.0.1:7870"
    endpoint_path: "/api/tts"
    voice_id: "mio"
    timeout_sec: 120
    checkpoint: "Aratako/Irodori-TTS-500M-v2"
    model_device: "mps"
    codec_device: "mps"
  sbv2:
    enabled: true
    base_url: "http://127.0.0.1:5000/synthesis"
    voice_id: "mio"
    timeout_sec: 20
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.TTS.Enabled {
		t.Fatal("expected tts enabled")
	}
	if got := cfg.TTS.SBV2.BaseURL; got != "http://127.0.0.1:5000/synthesis" {
		t.Fatalf("unexpected sbv2 base url: %s", got)
	}
	if got := cfg.TTS.Irodori.BaseURL; got != "http://127.0.0.1:7870" {
		t.Fatalf("unexpected irodori base url: %s", got)
	}
	if cfg.TTS.Irodori.EndpointPath != "/api/tts" || cfg.TTS.Irodori.Checkpoint != "Aratako/Irodori-TTS-500M-v2" || cfg.TTS.Irodori.NumSteps != 16 {
		t.Fatalf("unexpected irodori defaults: %+v", cfg.TTS.Irodori)
	}
	if len(cfg.TTS.PlaybackCommands) != 1 || cfg.TTS.PlaybackCommands[0].Name != "ffplay" {
		t.Fatalf("unexpected playback commands: %+v", cfg.TTS.PlaybackCommands)
	}
	if cfg.TTS.HTTPBaseURL == "" {
		t.Fatalf("expected tts http_base_url to be set")
	}
	if cfg.TTS.TimeoutMS != 15000 {
		t.Fatalf("unexpected tts timeout: %d", cfg.TTS.TimeoutMS)
	}
	if cfg.TTS.Speed != 1.2 {
		t.Fatalf("unexpected tts speed: %v", cfg.TTS.Speed)
	}
	if !cfg.TTS.TLSSkipVerify {
		t.Fatal("expected tts tls_skip_verify=true")
	}
	if cfg.TTS.ProviderParams["style"] != "Neutral" {
		t.Fatalf("unexpected provider_params: %+v", cfg.TTS.ProviderParams)
	}
}

func TestLoadConfig_STTSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "stt.yaml")
	content := `
server:
  port: 8443
  tls:
    enabled: true
    cert_file: "./certs/dev.crt"
    key_file: "./certs/dev.key"
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
stt:
  enabled: true
  provider: "external_http"
  language: "ja"
  model: "remote-stt"
  timeout_ms: 9000
  busy_policy: "queue_latest"
  endpoint_path: "/stt"
  vad: true
  debug:
    save_audio: false
    save_transcript: true
  external_http:
    url: "http://127.0.0.1:8080/inference"
    stream_url: "wss://127.0.0.1:8443/stt/stream"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.Server.TLS.Enabled || cfg.Server.TLS.CertFile == "" || cfg.Server.TLS.KeyFile == "" {
		t.Fatalf("expected TLS settings: %+v", cfg.Server.TLS)
	}
	if !cfg.STT.Enabled || cfg.STT.Provider != "external_http" || cfg.STT.Language != "ja" {
		t.Fatalf("unexpected stt config: %+v", cfg.STT)
	}
	if cfg.STT.TimeoutMS != 9000 || cfg.STT.ProviderURL != "http://127.0.0.1:8080/inference" {
		t.Fatalf("unexpected stt timeout/provider url: %+v", cfg.STT)
	}
	if cfg.STT.BusyPolicy != "queue_latest" {
		t.Fatalf("unexpected stt busy policy: %+v", cfg.STT)
	}
	if cfg.STT.StreamURL != "wss://127.0.0.1:8443/stt/stream" {
		t.Fatalf("unexpected stt stream url: %+v", cfg.STT)
	}
}

func TestLoadConfig_STTProviderURLBackwardCompatibility(t *testing.T) {
	t.Setenv("STT_PROVIDER_URL", "http://127.0.0.1:8080/inference")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "stt_env.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.STT.Provider != "external_http" || cfg.STT.ProviderURL != "http://127.0.0.1:8080/inference" {
		t.Fatalf("expected STT_PROVIDER_URL compatibility, got %+v", cfg.STT)
	}
}

func TestLoadConfig_TTSLocalHTTPSDefaultsToTLSSkipVerify(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "tts_local_https.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"
session:
  storage_dir: "./data/sessions"
tts:
  enabled: true
  http_base_url: "https://127.0.0.1:8770"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.TTS.TLSSkipVerify {
		t.Fatal("expected tls_skip_verify to auto-enable for local https")
	}
	if cfg.TTS.Speed != 1.2 {
		t.Fatalf("unexpected tts speed: %v", cfg.TTS.Speed)
	}
}

func TestLoadConfig_VTuberSettings(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "vtuber.yaml")

	content := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
session:
  storage_dir: "./data/sessions"
vtuber:
  enabled: true
  tick_interval_ms: 100
  characters:
    mio:
      audio_output: "Audio-Out-Mio"
      vts_host: "127.0.0.1"
      vts_port: 8001
      expression_map:
        happy: "ExpHappy"
        calm: "ExpCalm"
    shiro:
      audio_output: "Audio-Out-Shiro"
      vts_host: "127.0.0.1"
      vts_port: 8002
      expression_map:
        happy: "ExpHappy"
        calm: "ExpCalm"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if !cfg.VTuber.Enabled {
		t.Fatalf("expected vtuber enabled")
	}
	if cfg.VTuber.TickIntervalMS != 100 {
		t.Fatalf("expected tick interval 100, got %d", cfg.VTuber.TickIntervalMS)
	}
	if cfg.VTuber.ConnectTimeout != 3000 {
		t.Fatalf("expected default connect timeout 3000, got %d", cfg.VTuber.ConnectTimeout)
	}
	if cfg.VTuber.Characters["mio"].VTSPort != 8001 {
		t.Fatalf("expected mio port 8001, got %d", cfg.VTuber.Characters["mio"].VTSPort)
	}
	if cfg.VTuber.Characters["shiro"].ExpressionMap["happy"] != "ExpHappy" {
		t.Fatalf("expected shiro happy expression mapping")
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "Valid config",
			config: &Config{
				Server: ServerConfig{
					Port: 8080,
					Host: "0.0.0.0",
				},
				Ollama: OllamaConfig{
					BaseURL: "http://localhost:11434",
					Model:   "picoclaw-v1",
				},
				Session: SessionConfig{
					StorageDir: "./data/sessions",
				},
				ViewerLog: ViewerLogConfig{
					Enabled:           true,
					Path:              "./workspace/orchestrator_event_log.jsonl",
					RetentionDays:     14,
					GCIntervalMinutes: 60,
				},
				Coder1: CoderConfig{Name: "ao"},
				Coder2: CoderConfig{Name: "aka"},
				Coder3: CoderConfig{Name: "kin"},
				Coder4: CoderConfig{Name: "gin"},
			},
			wantErr: false,
		},
		{
			name: "Invalid security network_scope",
			config: &Config{
				Server:  ServerConfig{Port: 8080},
				Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
				Session: SessionConfig{StorageDir: "./data/sessions"},
				Security: SecurityConfig{
					Enabled:      true,
					PolicyMode:   "balanced",
					NetworkScope: "weird",
					Audit: SecurityAuditConfig{
						Backend: "jsonl",
						Path:    "logs/execution_audit.jsonl",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "Valid security policy_mode dev",
			config: &Config{
				Server:  ServerConfig{Port: 8080},
				Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
				Session: SessionConfig{StorageDir: "./data/sessions"},
				Security: SecurityConfig{
					Enabled:    true,
					PolicyMode: "dev",
					Audit: SecurityAuditConfig{
						Backend: "jsonl",
						Path:    "logs/execution_audit.jsonl",
					},
				},
				Coder1: CoderConfig{Name: "ao"},
				Coder2: CoderConfig{Name: "aka"},
				Coder3: CoderConfig{Name: "kin"},
				Coder4: CoderConfig{Name: "gin"},
			},
			wantErr: false,
		},
		{
			name: "Invalid port (too low)",
			config: &Config{
				Server: ServerConfig{
					Port: 0,
				},
			},
			wantErr: true,
		},
		{
			name: "Invalid port (too high)",
			config: &Config{
				Server: ServerConfig{
					Port: 70000,
				},
			},
			wantErr: true,
		},
		{
			name: "Invalid viewer log retention",
			config: &Config{
				Server:  ServerConfig{Port: 8080},
				Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
				Session: SessionConfig{StorageDir: "./data/sessions"},
				ViewerLog: ViewerLogConfig{
					Enabled:           true,
					Path:              "./workspace/orchestrator_event_log.jsonl",
					RetentionDays:     0,
					GCIntervalMinutes: 60,
				},
			},
			wantErr: true,
		},
		{
			name: "Missing Ollama base URL",
			config: &Config{
				Server: ServerConfig{
					Port: 8080,
				},
				Ollama: OllamaConfig{
					BaseURL: "",
				},
			},
			wantErr: true,
		},
		{
			name: "Missing Ollama model",
			config: &Config{
				Server: ServerConfig{
					Port: 8080,
				},
				Ollama: OllamaConfig{
					BaseURL: "http://localhost:11434",
					Model:   "",
				},
			},
			wantErr: true,
		},
		{
			name: "Missing session storage dir",
			config: &Config{
				Server: ServerConfig{
					Port: 8080,
				},
				Ollama: OllamaConfig{
					BaseURL: "http://localhost:11434",
					Model:   "picoclaw-v1",
				},
				Session: SessionConfig{
					StorageDir: "",
				},
			},
			wantErr: true,
		},
		{
			name: "Valid local LLM without Ollama",
			config: &Config{
				Server: ServerConfig{Port: 8080},
				LocalLLM: LocalLLMConfig{
					Enabled:           true,
					Provider:          "local_openai",
					BaseURL:           "http://127.0.0.1:8080",
					ChatModel:         "Chat",
					WorkerModel:       "Worker",
					HeavyModel:        "Heavy",
					WildModel:         "Wild",
					TimeoutSec:        120,
					GlobalConcurrency: 2,
					ModelConcurrency:  1,
				},
				Session: SessionConfig{StorageDir: "./data/sessions"},
				Coder1:  CoderConfig{Name: "ao"},
				Coder2:  CoderConfig{Name: "aka"},
				Coder3:  CoderConfig{Name: "kin"},
				Coder4:  CoderConfig{Name: "gin"},
			},
			wantErr: false,
		},
		{
			name: "Local LLM missing base URL",
			config: &Config{
				Server: ServerConfig{Port: 8080},
				LocalLLM: LocalLLMConfig{
					Enabled:           true,
					Provider:          "local_openai",
					ChatModel:         "Chat",
					WorkerModel:       "Worker",
					HeavyModel:        "Heavy",
					WildModel:         "Wild",
					TimeoutSec:        120,
					GlobalConcurrency: 2,
					ModelConcurrency:  1,
				},
				Session: SessionConfig{StorageDir: "./data/sessions"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_Validate_Distributed(t *testing.T) {
	base := func() *Config {
		cfg := &Config{
			Server:  ServerConfig{Port: 8080},
			Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
			Session: SessionConfig{StorageDir: "./data"},
		}
		// Coder1-4 の最小限の設定（バリデーションを通すため）
		cfg.Coder1.Name = "ao"
		cfg.Coder2.Name = "aka"
		cfg.Coder3.Name = "kin"
		cfg.Coder4.Name = "gin"
		return cfg
	}

	t.Run("Distributed enabled without transports", func(t *testing.T) {
		cfg := base()
		cfg.Distributed.Enabled = true
		if err := cfg.Validate(); err == nil {
			t.Error("Expected error for distributed without transports")
		}
	})

	t.Run("Distributed with invalid transport type", func(t *testing.T) {
		cfg := base()
		cfg.Distributed.Enabled = true
		cfg.Distributed.Transports = map[string]TransportConfig{
			"mio": {Type: "invalid"},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("Expected error for invalid transport type")
		}
	})

	t.Run("Distributed SSH missing remote_host", func(t *testing.T) {
		cfg := base()
		cfg.Distributed.Enabled = true
		cfg.Distributed.Transports = map[string]TransportConfig{
			"coder3": {Type: "ssh", RemoteUser: "picoclaw", SSHKeyPath: "/path"},
		}
		if err := cfg.Validate(); err == nil {
			t.Error("Expected error for SSH missing remote_host")
		}
	})

	t.Run("Distributed valid config", func(t *testing.T) {
		cfg := base()
		cfg.Distributed.Enabled = true
		cfg.Distributed.Transports = map[string]TransportConfig{
			"mio":    {Type: "local"},
			"coder3": {Type: "ssh", RemoteHost: "192.168.1.100:22", RemoteUser: "picoclaw", SSHKeyPath: "/path"},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Expected valid config, got error: %v", err)
		}
	})
}

func TestConfig_Validate_IdleChat(t *testing.T) {
	base := func() *Config {
		cfg := &Config{
			Server:  ServerConfig{Port: 8080},
			Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
			Session: SessionConfig{StorageDir: "./data"},
		}
		// Coder1-4 の最小限の設定（バリデーションを通すため）
		cfg.Coder1.Name = "ao"
		cfg.Coder2.Name = "aka"
		cfg.Coder3.Name = "kin"
		cfg.Coder4.Name = "gin"
		return cfg
	}

	t.Run("IdleChat with unknown agent", func(t *testing.T) {
		cfg := base()
		cfg.IdleChat.Enabled = true
		cfg.IdleChat.Participants = []string{"mio", "Unknown"}
		cfg.IdleChat.IntervalMin = 5
		cfg.IdleChat.MaxTurns = 10
		cfg.IdleChat.Temperature = 0.8
		if err := cfg.Validate(); err == nil {
			t.Error("Expected error for unknown agent")
		}
	})

	t.Run("IdleChat with invalid max_turns", func(t *testing.T) {
		cfg := base()
		cfg.IdleChat.Enabled = true
		cfg.IdleChat.Participants = []string{"mio", "shiro"}
		cfg.IdleChat.IntervalMin = 5
		cfg.IdleChat.MaxTurns = 200
		cfg.IdleChat.Temperature = 0.8
		if err := cfg.Validate(); err == nil {
			t.Error("Expected error for max_turns > 100")
		}
	})

	t.Run("IdleChat valid config", func(t *testing.T) {
		cfg := base()
		cfg.IdleChat.Enabled = true
		cfg.IdleChat.Participants = []string{"mio", "shiro"}
		cfg.IdleChat.IntervalMin = 5
		cfg.IdleChat.MaxTurns = 10
		cfg.IdleChat.Temperature = 0.8
		if err := cfg.Validate(); err != nil {
			t.Errorf("Expected valid config, got error: %v", err)
		}
	})
}

func TestLoadConfig_IdleChatDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"

idle_chat:
  enabled: true
`

	os.WriteFile(configPath, []byte(configContent), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if len(cfg.IdleChat.Participants) != 2 {
		t.Errorf("Expected 2 default participants, got %d", len(cfg.IdleChat.Participants))
	}

	if cfg.IdleChat.IntervalMin != 5 {
		t.Errorf("Expected IntervalMin 5, got %d", cfg.IdleChat.IntervalMin)
	}
	if cfg.IdleChat.IntervalSec != 300 {
		t.Errorf("Expected IntervalSec 300, got %d", cfg.IdleChat.IntervalSec)
	}

	if cfg.IdleChat.MaxTurns != 10 {
		t.Errorf("Expected MaxTurns 10, got %d", cfg.IdleChat.MaxTurns)
	}

	if cfg.IdleChat.Temperature != 0.8 {
		t.Errorf("Expected Temperature 0.8, got %f", cfg.IdleChat.Temperature)
	}
	if cfg.IdleChat.SpeakerLLMOptions["mio"].Think == nil || *cfg.IdleChat.SpeakerLLMOptions["mio"].Think {
		t.Fatalf("Expected idle_chat speaker mio think default false, got %#v", cfg.IdleChat.SpeakerLLMOptions["mio"].Think)
	}
	if cfg.IdleChat.SpeakerLLMOptions["shiro"].Think == nil || *cfg.IdleChat.SpeakerLLMOptions["shiro"].Think {
		t.Fatalf("Expected idle_chat speaker shiro think default false, got %#v", cfg.IdleChat.SpeakerLLMOptions["shiro"].Think)
	}
	if !cfg.IdleChat.TopicGeneration.Enabled {
		t.Fatal("Expected idle_chat topic_generation default enabled")
	}
	if cfg.IdleChat.TopicGeneration.CandidatesPerAttempt != 5 {
		t.Fatalf("Expected topic_generation candidates_per_attempt 5, got %d", cfg.IdleChat.TopicGeneration.CandidatesPerAttempt)
	}
	if cfg.IdleChat.TopicGeneration.MaxAttempts != 3 {
		t.Fatalf("Expected topic_generation max_attempts 3, got %d", cfg.IdleChat.TopicGeneration.MaxAttempts)
	}
	if !cfg.IdleChat.TopicGeneration.JudgeEnabled {
		t.Fatal("Expected topic_generation judge_enabled default true")
	}
	if cfg.IdleChat.TopicGeneration.Prompts.Judge != "prompts/idle_chat/topic_judge.md" {
		t.Fatalf("Expected topic judge prompt default, got %q", cfg.IdleChat.TopicGeneration.Prompts.Judge)
	}
	if !cfg.IdleChat.DialogueInterestingness.Enabled {
		t.Fatal("Expected idle_chat dialogue_interestingness default enabled")
	}
	if cfg.IdleChat.DialogueInterestingness.MaxTurnsPerTopic != 12 {
		t.Fatalf("Expected dialogue max_turns_per_topic 12, got %d", cfg.IdleChat.DialogueInterestingness.MaxTurnsPerTopic)
	}
	if cfg.IdleChat.DialogueInterestingness.MinQualityScore != 70 {
		t.Fatalf("Expected dialogue min_quality_score 70, got %d", cfg.IdleChat.DialogueInterestingness.MinQualityScore)
	}
	if cfg.IdleChat.DialogueInterestingness.Prompts.Common != "prompts/idle_chat/dialogue_common.md" {
		t.Fatalf("Expected dialogue common prompt default, got %q", cfg.IdleChat.DialogueInterestingness.Prompts.Common)
	}
}

func TestLoadConfig_IdleChatOtherSpeakersDefaultThinkTrue(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"

idle_chat:
  enabled: true
  participants:
    - mio
    - shiro
    - aka
`

	os.WriteFile(configPath, []byte(configContent), 0644)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.IdleChat.SpeakerLLMOptions["aka"].Think == nil || !*cfg.IdleChat.SpeakerLLMOptions["aka"].Think {
		t.Fatalf("Expected idle_chat speaker aka think default true, got %#v", cfg.IdleChat.SpeakerLLMOptions["aka"].Think)
	}
}

func TestConversationConfig_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "Chat"
session:
  storage_dir: "./data"
`
	os.WriteFile(configPath, []byte(configContent), 0644)
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// デフォルト値確認
	if cfg.Conversation.RedisURL != "redis://localhost:6379" {
		t.Errorf("unexpected RedisURL: %s", cfg.Conversation.RedisURL)
	}
	if cfg.Conversation.VectorDBURL != "localhost:6334" {
		t.Errorf("unexpected VectorDBURL: %s", cfg.Conversation.VectorDBURL)
	}
}

func TestConversationConfig_EmbedAndSummaryModel(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
server:
  port: 8080
ollama:
  base_url: "http://localhost:11434"
  model: "Chat"
session:
  storage_dir: "./data"
conversation:
  enabled: true
  redis_url: "redis://localhost:6379"
  vectordb_url: "localhost:6334"
  vector_collection: "picoclaw_memory_3584"
  vector_dimension: 3584
  embed_provider: "ollama"
  embed_base_url: "http://localhost:11434"
  embed_model: "nomic-embed-text"
  summary_model: "Chat"
`
	os.WriteFile(configPath, []byte(configContent), 0644)
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Conversation.EmbedModel != "nomic-embed-text" {
		t.Errorf("expected EmbedModel 'nomic-embed-text', got %q", cfg.Conversation.EmbedModel)
	}
	if cfg.Conversation.VectorCollection != "picoclaw_memory_3584" {
		t.Errorf("expected VectorCollection 'picoclaw_memory_3584', got %q", cfg.Conversation.VectorCollection)
	}
	if cfg.Conversation.VectorDimension != 3584 {
		t.Errorf("expected VectorDimension 3584, got %d", cfg.Conversation.VectorDimension)
	}
	if cfg.Conversation.EmbedProvider != "ollama" {
		t.Errorf("expected EmbedProvider 'ollama', got %q", cfg.Conversation.EmbedProvider)
	}
	if cfg.Conversation.EmbedBaseURL != "http://localhost:11434" {
		t.Errorf("expected EmbedBaseURL 'http://localhost:11434', got %q", cfg.Conversation.EmbedBaseURL)
	}
	if cfg.Conversation.SummaryModel != "Chat" {
		t.Errorf("expected SummaryModel 'Chat', got %q", cfg.Conversation.SummaryModel)
	}
}

// TestGlossaryConfig_DefaultValues はGlossaryConfigのデフォルト値を検証
func TestGlossaryConfig_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Glossaryセクションを含まないミニマルな設定
	minimalContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"
`

	err := os.WriteFile(configPath, []byte(minimalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// デフォルト値の検証（config.go:457-472 の実装と一致）
	if cfg.Glossary.DBPath != "./workspace/glossary.db" {
		t.Errorf("Expected Glossary.DBPath './workspace/glossary.db', got '%s'", cfg.Glossary.DBPath)
	}

	if cfg.Glossary.RefreshIntervalHr != 6 {
		t.Errorf("Expected Glossary.RefreshIntervalHr 6, got %d", cfg.Glossary.RefreshIntervalHr)
	}

	if cfg.Glossary.MaxEntries != 8 {
		t.Errorf("Expected Glossary.MaxEntries 8, got %d", cfg.Glossary.MaxEntries)
	}

	if len(cfg.Glossary.FeedURLs) != 3 {
		t.Errorf("Expected 3 default FeedURLs, got %d", len(cfg.Glossary.FeedURLs))
	}

	expectedFeeds := []string{
		"https://www3.nhk.or.jp/rss/news/cat0.xml",
		"https://feeds.bbci.co.uk/news/world/rss.xml",
		"https://feeds.bbci.co.uk/news/technology/rss.xml",
	}

	for i, expectedURL := range expectedFeeds {
		if i >= len(cfg.Glossary.FeedURLs) {
			t.Errorf("FeedURLs[%d] is missing", i)
			continue
		}
		if cfg.Glossary.FeedURLs[i] != expectedURL {
			t.Errorf("FeedURLs[%d]: expected '%s', got '%s'", i, expectedURL, cfg.Glossary.FeedURLs[i])
		}
	}
}

// TestGlossaryConfig_CustomValues はカスタム値が正しく読み込まれることを検証
func TestGlossaryConfig_CustomValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	customContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"

glossary:
  enabled: true
  db_path: "/custom/path/glossary.db"
  refresh_interval_hr: 12
  max_entries: 20
  feed_urls:
    - "https://custom.feed/rss"
`

	err := os.WriteFile(configPath, []byte(customContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// カスタム値の検証
	if !cfg.Glossary.Enabled {
		t.Error("Expected Glossary.Enabled true")
	}

	if cfg.Glossary.DBPath != "/custom/path/glossary.db" {
		t.Errorf("Expected custom DBPath, got '%s'", cfg.Glossary.DBPath)
	}

	if cfg.Glossary.RefreshIntervalHr != 12 {
		t.Errorf("Expected RefreshIntervalHr 12, got %d", cfg.Glossary.RefreshIntervalHr)
	}

	if cfg.Glossary.MaxEntries != 20 {
		t.Errorf("Expected MaxEntries 20, got %d", cfg.Glossary.MaxEntries)
	}

	if len(cfg.Glossary.FeedURLs) != 1 {
		t.Errorf("Expected 1 custom FeedURL, got %d", len(cfg.Glossary.FeedURLs))
	}

	if len(cfg.Glossary.FeedURLs) > 0 && cfg.Glossary.FeedURLs[0] != "https://custom.feed/rss" {
		t.Errorf("Expected custom feed URL, got '%s'", cfg.Glossary.FeedURLs[0])
	}
}

// TestCoderConfig_DefaultValues は Coder1-4 のデフォルト値を検証
func TestCoderConfig_DefaultValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Coder セクションを含まないミニマルな設定
	minimalContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"
`

	err := os.WriteFile(configPath, []byte(minimalContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Coder1 デフォルト値検証
	if cfg.Coder1.Provider != "deepseek" {
		t.Errorf("Coder1.Provider: expected 'deepseek', got '%s'", cfg.Coder1.Provider)
	}
	if cfg.Coder1.Model != "deepseek-coder" {
		t.Errorf("Coder1.Model: expected 'deepseek-coder', got '%s'", cfg.Coder1.Model)
	}
	if cfg.Coder1.Name != "ao" {
		t.Errorf("Coder1.Name: expected 'ao', got '%s'", cfg.Coder1.Name)
	}
	if cfg.Coder1.DisplayName != "青" {
		t.Errorf("Coder1.DisplayName: expected '青', got '%s'", cfg.Coder1.DisplayName)
	}
	if cfg.Coder1.LightMemory.MaxTurns != 3 {
		t.Errorf("Coder1.LightMemory.MaxTurns: expected 3, got %d", cfg.Coder1.LightMemory.MaxTurns)
	}

	// Coder2 デフォルト値検証
	if cfg.Coder2.Provider != "openai" {
		t.Errorf("Coder2.Provider: expected 'openai', got '%s'", cfg.Coder2.Provider)
	}
	if cfg.Coder2.Model != "gpt-4-turbo" {
		t.Errorf("Coder2.Model: expected 'gpt-4-turbo', got '%s'", cfg.Coder2.Model)
	}
	if cfg.Coder2.Name != "aka" {
		t.Errorf("Coder2.Name: expected 'aka', got '%s'", cfg.Coder2.Name)
	}
	if cfg.Coder2.DisplayName != "赤" {
		t.Errorf("Coder2.DisplayName: expected '赤', got '%s'", cfg.Coder2.DisplayName)
	}

	// Coder3 デフォルト値検証
	if cfg.Coder3.Provider != "claude" {
		t.Errorf("Coder3.Provider: expected 'claude', got '%s'", cfg.Coder3.Provider)
	}
	if cfg.Coder3.Model != "claude-sonnet-4" {
		t.Errorf("Coder3.Model: expected 'claude-sonnet-4', got '%s'", cfg.Coder3.Model)
	}
	if cfg.Coder3.Name != "kin" {
		t.Errorf("Coder3.Name: expected 'kin', got '%s'", cfg.Coder3.Name)
	}
	if cfg.Coder3.DisplayName != "金" {
		t.Errorf("Coder3.DisplayName: expected '金', got '%s'", cfg.Coder3.DisplayName)
	}

	// Coder4 デフォルト値検証
	if cfg.Coder4.Provider != "gemini" {
		t.Errorf("Coder4.Provider: expected 'gemini', got '%s'", cfg.Coder4.Provider)
	}
	if cfg.Coder4.Model != "gemini-2.0-flash-exp" {
		t.Errorf("Coder4.Model: expected 'gemini-2.0-flash-exp', got '%s'", cfg.Coder4.Model)
	}
	if cfg.Coder4.Name != "gin" {
		t.Errorf("Coder4.Name: expected 'gin', got '%s'", cfg.Coder4.Name)
	}
	if cfg.Coder4.DisplayName != "銀" {
		t.Errorf("Coder4.DisplayName: expected '銀', got '%s'", cfg.Coder4.DisplayName)
	}
}

// TestCoderConfig_CustomValues はカスタム値が正しく読み込まれることを検証
func TestCoderConfig_CustomValues(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	customContent := `
server:
  port: 8080

ollama:
  base_url: "http://localhost:11434"
  model: "picoclaw-v1"

session:
  storage_dir: "./data/sessions"

coder1:
  name: "custom_ao"
  display_name: "カスタム青"
  provider: "deepseek"
  model: "deepseek-custom"
  api_key: "test-key-1"
  personality: "あなたはカスタム青。設計思考が得意。"
  tone: "analytical"
  light_memory:
    enabled: true
    max_turns: 5
  enabled: true

coder4:
  name: "custom_gin"
  display_name: "カスタム銀"
  provider: "gemini"
  model: "gemini-pro"
  api_key: "test-key-4"
  personality: "あなたはカスタム銀。"
  tone: "fast"
  light_memory:
    enabled: true
    max_turns: 10
  enabled: true
`

	err := os.WriteFile(configPath, []byte(customContent), 0644)
	if err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	// Coder1 カスタム値検証
	if cfg.Coder1.Name != "custom_ao" {
		t.Errorf("Coder1.Name: expected 'custom_ao', got '%s'", cfg.Coder1.Name)
	}
	if cfg.Coder1.DisplayName != "カスタム青" {
		t.Errorf("Coder1.DisplayName: expected 'カスタム青', got '%s'", cfg.Coder1.DisplayName)
	}
	if cfg.Coder1.Model != "deepseek-custom" {
		t.Errorf("Coder1.Model: expected 'deepseek-custom', got '%s'", cfg.Coder1.Model)
	}
	if cfg.Coder1.APIKey != "test-key-1" {
		t.Errorf("Coder1.APIKey: expected 'test-key-1', got '%s'", cfg.Coder1.APIKey)
	}
	if cfg.Coder1.Personality != "あなたはカスタム青。設計思考が得意。" {
		t.Errorf("Coder1.Personality: unexpected value '%s'", cfg.Coder1.Personality)
	}
	if cfg.Coder1.Tone != "analytical" {
		t.Errorf("Coder1.Tone: expected 'analytical', got '%s'", cfg.Coder1.Tone)
	}
	if !cfg.Coder1.LightMemory.Enabled {
		t.Error("Coder1.LightMemory.Enabled: expected true")
	}
	if cfg.Coder1.LightMemory.MaxTurns != 5 {
		t.Errorf("Coder1.LightMemory.MaxTurns: expected 5, got %d", cfg.Coder1.LightMemory.MaxTurns)
	}
	if !cfg.Coder1.Enabled {
		t.Error("Coder1.Enabled: expected true")
	}

	// Coder4 カスタム値検証
	if cfg.Coder4.Name != "custom_gin" {
		t.Errorf("Coder4.Name: expected 'custom_gin', got '%s'", cfg.Coder4.Name)
	}
	if cfg.Coder4.DisplayName != "カスタム銀" {
		t.Errorf("Coder4.DisplayName: expected 'カスタム銀', got '%s'", cfg.Coder4.DisplayName)
	}
	if cfg.Coder4.Provider != "gemini" {
		t.Errorf("Coder4.Provider: expected 'gemini', got '%s'", cfg.Coder4.Provider)
	}
	if cfg.Coder4.Model != "gemini-pro" {
		t.Errorf("Coder4.Model: expected 'gemini-pro', got '%s'", cfg.Coder4.Model)
	}
	if cfg.Coder4.APIKey != "test-key-4" {
		t.Errorf("Coder4.APIKey: expected 'test-key-4', got '%s'", cfg.Coder4.APIKey)
	}
	if cfg.Coder4.LightMemory.MaxTurns != 10 {
		t.Errorf("Coder4.LightMemory.MaxTurns: expected 10, got %d", cfg.Coder4.LightMemory.MaxTurns)
	}
}

// TestValidateCoderConfig は validateCoderConfig() 関数を直接テスト
func TestValidateCoderConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  CoderConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "有効な設定",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "test-key",
				Enabled:  true,
			},
			wantErr: false,
		},
		{
			name: "無効な provider",
			config: CoderConfig{
				Name:     "test",
				Provider: "invalid-provider",
				Model:    "test-model",
				Enabled:  true,
			},
			wantErr: true,
			errMsg:  "provider must be one of",
		},
		{
			name: "name が空",
			config: CoderConfig{
				Name:     "",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "test-key",
				Enabled:  true,
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "enabled=true だが model が空",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "",
				APIKey:   "test-key",
				Enabled:  true,
			},
			wantErr: true,
			errMsg:  "model is required",
		},
		{
			name: "enabled=true だが api_key が空（deepseek）",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "",
				Enabled:  true,
			},
			wantErr: true,
			errMsg:  "api_key is required",
		},
		{
			name: "enabled=true だが base_url が空（ollama）",
			config: CoderConfig{
				Name:     "test",
				Provider: "ollama",
				Model:    "test-model",
				BaseURL:  "",
				Enabled:  true,
			},
			wantErr: true,
			errMsg:  "base_url is required",
		},
		{
			name: "light_memory max_turns が範囲外（大きすぎ）",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "test-key",
				LightMemory: LightMemoryConfig{
					Enabled:  true,
					MaxTurns: 100,
				},
				Enabled: true,
			},
			wantErr: true,
			errMsg:  "max_turns must be between 1 and 20",
		},
		{
			name: "light_memory max_turns が範囲外（0）",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "test-key",
				LightMemory: LightMemoryConfig{
					Enabled:  true,
					MaxTurns: 0,
				},
				Enabled: true,
			},
			wantErr: true,
			errMsg:  "max_turns must be between 1 and 20",
		},
		{
			name: "enabled=false の場合は api_key なしでも OK",
			config: CoderConfig{
				Name:     "test",
				Provider: "deepseek",
				Model:    "test-model",
				APIKey:   "",
				Enabled:  false,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCoderConfig("test_coder", &tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
				} else if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("Expected error containing '%s', got: %v", tt.errMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error, got: %v", err)
				}
			}
		})
	}
}

func TestConfig_Validate_LLMOps(t *testing.T) {
	base := func() *Config {
		cfg := &Config{
			Server:  ServerConfig{Port: 8080},
			Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
			Session: SessionConfig{StorageDir: "./data"},
		}
		cfg.Coder1.Name = "ao"
		cfg.Coder2.Name = "aka"
		cfg.Coder3.Name = "kin"
		cfg.Coder4.Name = "gin"
		return cfg
	}
	t.Run("enabled without base_url", func(t *testing.T) {
		cfg := base()
		cfg.LLMOps.Enabled = true
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("enabled with base_url", func(t *testing.T) {
		cfg := base()
		cfg.LLMOps.Enabled = true
		cfg.LLMOps.BaseURL = "http://192.168.1.31:8079"
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestConfig_Validate_BrowserActor(t *testing.T) {
	base := func() *Config {
		cfg := &Config{
			Server:  ServerConfig{Port: 8080},
			Ollama:  OllamaConfig{BaseURL: "http://localhost:11434", Model: "picoclaw-v1"},
			Session: SessionConfig{StorageDir: "./data"},
		}
		cfg.Coder1.Name = "ao"
		cfg.Coder2.Name = "aka"
		cfg.Coder3.Name = "kin"
		cfg.Coder4.Name = "gin"
		cfg.setDefaults()
		return cfg
	}
	t.Run("defaults are valid", func(t *testing.T) {
		cfg := base()
		wantToolsRoot := filepath.Join(userHomeDirForTest(t), "RenCrow", "RenCrow_Tools")
		if cfg.BrowserActor.RunnerPath != filepath.Join(wantToolsRoot, "tools", "browser_actor", "run_browser_actor.mjs") {
			t.Fatalf("unexpected browser actor runner path: %s", cfg.BrowserActor.RunnerPath)
		}
		if !cfg.BrowserActor.HeadlessDefaultEnabled() || !cfg.BrowserActor.SaveTraceEnabled() || !cfg.BrowserActor.SaveScreenshotEnabled() || !cfg.BrowserActor.MaskSecretsEnabled() {
			t.Fatalf("browser actor safe defaults not applied: %+v", cfg.BrowserActor)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("enabled validates max actions", func(t *testing.T) {
		cfg := base()
		cfg.BrowserActor.Enabled = true
		cfg.BrowserActor.MaxActions = 101
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "browser_actor.max_actions") {
			t.Fatalf("expected browser_actor.max_actions error, got %v", err)
		}
	})
	t.Run("path traversal rejected", func(t *testing.T) {
		cfg := base()
		cfg.BrowserActor.ArtifactRoot = "../escape"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "browser_actor paths") {
			t.Fatalf("expected browser_actor path error, got %v", err)
		}
	})
}
