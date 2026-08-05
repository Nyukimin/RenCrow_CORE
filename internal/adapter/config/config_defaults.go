package config

import (
	"os"
	"path/filepath"
	"strings"
)

// setDefaults はデフォルト値を設定
func (c *Config) setDefaults() {
	if c.Server.Host == "" {
		c.Server.Host = "0.0.0.0"
	}

	// RenCrow_LLM is mandatory.
	c.LLMGateway.Enabled = true
	if strings.TrimSpace(c.LLMGateway.BaseURL) == "" {
		c.LLMGateway.BaseURL = "http://127.0.0.1:8090"
	}
	if c.LLMGateway.TimeoutSec <= 0 {
		c.LLMGateway.TimeoutSec = 600
	}
	if c.Mio.Generation.MaxTokens <= 0 {
		c.Mio.Generation.MaxTokens = 512
	}
	if c.Mio.Generation.Temperature <= 0 {
		c.Mio.Generation.Temperature = 0.7
	}
	if strings.TrimSpace(c.Mio.InputAudio.Prompt) == "" {
		c.Mio.InputAudio.Prompt = "音声の内容を理解し、日本語で短く自然に返答してください。"
	}
	if c.WebwrightFetch.RunnerPath == "" {
		c.WebwrightFetch.RunnerPath = defaultRenCrowToolsPath("tools", "webwright_fetch", "run_webwright_fetch.py")
	}
	if c.WebwrightFetch.ConfigPath == "" {
		c.WebwrightFetch.ConfigPath = defaultRenCrowToolsPath("tools", "webwright_fetch", "config_local_worker.yaml")
	}
	if c.WebwrightFetch.OutputDir == "" {
		c.WebwrightFetch.OutputDir = "tmp/webwright_runs"
	}
	if c.WebwrightFetch.StagingOutputDir == "" {
		c.WebwrightFetch.StagingOutputDir = "tmp/webwright_staging"
	}
	// Webwright also enters RenCrow_LLM.
	c.WebwrightFetch.ResponsesEndpoint = strings.TrimRight(c.LLMGateway.BaseURL, "/") + "/v1/responses"
	if c.WebwrightFetch.Model == "" {
		c.WebwrightFetch.Model = "coder1"
	}
	if envName := strings.TrimSpace(c.LLMGateway.APIKeyEnv); envName != "" {
		c.WebwrightFetch.APIKey = strings.TrimSpace(os.Getenv(envName))
	}
	if c.WebwrightFetch.APIKey == "" {
		c.WebwrightFetch.APIKey = "dummy"
	}
	if c.BrowserActor.RunnerPath == "" {
		c.BrowserActor.RunnerPath = defaultRenCrowToolsPath("tools", "browser_actor", "run_browser_actor.mjs")
	}
	if c.BrowserActor.NodeBinary == "" {
		c.BrowserActor.NodeBinary = "node"
	}
	if c.BrowserActor.Browser == "" {
		c.BrowserActor.Browser = "chromium"
	}
	if c.BrowserActor.ProfileRoot == "" {
		c.BrowserActor.ProfileRoot = "workspace/browser_profiles"
	}
	if c.BrowserActor.ArtifactRoot == "" {
		c.BrowserActor.ArtifactRoot = "workspace/browser_runs"
	}
	if c.BrowserActor.TimeoutMS <= 0 {
		c.BrowserActor.TimeoutMS = 30000
	}
	if c.BrowserActor.MaxActions <= 0 {
		c.BrowserActor.MaxActions = 30
	}
	if c.BrowserActor.NetworkScope == "" {
		c.BrowserActor.NetworkScope = "allowlist"
	}
	if len(c.BrowserActor.AllowedOrigins) == 0 {
		c.BrowserActor.AllowedOrigins = []string{"http://127.0.0.1:18790", "http://localhost:18790", "file://"}
	}
	if c.BrowserActor.HeadlessDefault == nil {
		c.BrowserActor.HeadlessDefault = boolConfigPtr(true)
	}
	if c.BrowserActor.SaveTrace == nil {
		c.BrowserActor.SaveTrace = boolConfigPtr(true)
	}
	if c.BrowserActor.SaveScreenshot == nil {
		c.BrowserActor.SaveScreenshot = boolConfigPtr(true)
	}
	if c.BrowserActor.MaskSecrets == nil {
		c.BrowserActor.MaskSecrets = boolConfigPtr(true)
	}
	if c.Codex.Command == "" {
		c.Codex.Command = "codex"
	}
	if c.Codex.Sandbox == "" {
		c.Codex.Sandbox = "read-only"
	}
	if c.Codex.TimeoutMS <= 0 {
		c.Codex.TimeoutMS = 600000
	}
	if c.Codex.MaxPromptBytes <= 0 {
		c.Codex.MaxPromptBytes = 65536
	}
	if c.Codex.MaxOutputBytes <= 0 {
		c.Codex.MaxOutputBytes = 1048576
	}
	if c.Codex.Ephemeral == nil {
		c.Codex.Ephemeral = boolConfigPtr(true)
	}

	if c.Log.Level == "" {
		c.Log.Level = "info"
	}

	if c.Log.Format == "" {
		c.Log.Format = "json"
	}

	// Worker設定デフォルト
	if c.Worker.CommitMessagePrefix == "" {
		c.Worker.CommitMessagePrefix = "[Worker Auto-Commit]"
	}

	if c.Worker.CommandTimeout == 0 {
		c.Worker.CommandTimeout = 300 // 5分
	}

	if c.Worker.GitTimeout == 0 {
		c.Worker.GitTimeout = 30 // 30秒
	}

	if len(c.Worker.ProtectedPatterns) == 0 {
		c.Worker.ProtectedPatterns = []string{".env*", "*credentials*", "*.key", "*.pem"}
	}

	if c.Worker.ActionOnProtected == "" {
		c.Worker.ActionOnProtected = "error"
	}

	if c.Worker.Workspace == "" {
		c.Worker.Workspace = "." // カレントディレクトリ
	}

	// v4.0 Worker並列実行デフォルト
	if c.Worker.MaxParallelism == 0 {
		c.Worker.MaxParallelism = 4
	}
	if c.Worker.LightMemory.MaxTurns == 0 {
		c.Worker.LightMemory.MaxTurns = 3
	}

	// v4.0 IdleChat デフォルト
	if c.IdleChat.Enabled {
		if len(c.IdleChat.Participants) == 0 {
			c.IdleChat.Participants = []string{"mio", "shiro"}
		}
		if c.IdleChat.IntervalMin == 0 {
			c.IdleChat.IntervalMin = 5
		}
		if c.IdleChat.IntervalSec == 0 {
			c.IdleChat.IntervalSec = c.IdleChat.IntervalMin * 60
		}
		if c.IdleChat.MaxTurns == 0 {
			c.IdleChat.MaxTurns = 10
		}
		if c.IdleChat.Temperature == 0 {
			c.IdleChat.Temperature = 0.8
		}
		c.applyIdleChatTopicGenerationDefaults()
		c.applyIdleChatDialogueInterestingnessDefaults()
		c.applyIdleChatSpeakerLLMDefaults()
		c.applyIdleChatNewsSourceDefaults()
		c.applyIdleChatEpisodePreparationDefaults()
	}

	// v5.0 Conversation デフォルト
	// enabled: false がデフォルト（明示的に有効化が必要）
	if c.Conversation.RedisURL == "" {
		c.Conversation.RedisURL = "redis://localhost:6379"
	}
	if c.Storage.Databases.ConversationArchive == "" {
		c.Storage.Databases.ConversationArchive = "/var/lib/rencrow/memory_archive.db"
	}
	if c.Conversation.VectorDBURL == "" {
		c.Conversation.VectorDBURL = "localhost:6334"
	}
	if c.Conversation.ProfilePromotionIdleGraceSeconds == 0 {
		c.Conversation.ProfilePromotionIdleGraceSeconds = 10
	}
	if c.Conversation.ProfilePromotionTimeoutSeconds == 0 {
		c.Conversation.ProfilePromotionTimeoutSeconds = 45
	}
	if c.Conversation.ProfilePromotionBatchMessages == 0 {
		c.Conversation.ProfilePromotionBatchMessages = 24
	}
	if c.Conversation.ProfilePromotionMaxAttempts == 0 {
		c.Conversation.ProfilePromotionMaxAttempts = 5
	}

	// Heartbeat デフォルト
	if c.Heartbeat.Interval == 0 {
		c.Heartbeat.Interval = 30
	}
	if c.Heartbeat.XBookmarks.IntervalMinutes == 0 {
		c.Heartbeat.XBookmarks.IntervalMinutes = 360
	}
	if c.Heartbeat.XBookmarks.TimeoutMinutes == 0 {
		c.Heartbeat.XBookmarks.TimeoutMinutes = 90
	}
	if c.Heartbeat.XBookmarks.RunOnStart == nil {
		c.Heartbeat.XBookmarks.RunOnStart = boolConfigPtr(true)
	}
	if strings.TrimSpace(c.Heartbeat.XBookmarks.Command) == "" {
		c.Heartbeat.XBookmarks.Command = "rencrow-x-bookmarks"
	}
	if c.Heartbeat.XBookmarks.MaxScrolls == nil {
		maxScrolls := 100
		c.Heartbeat.XBookmarks.MaxScrolls = &maxScrolls
	}

	if c.Glossary.DBPath == "" {
		c.Glossary.DBPath = "./workspace/glossary.db"
	}
	if c.Glossary.RefreshIntervalHr == 0 {
		c.Glossary.RefreshIntervalHr = 6
	}
	if c.Glossary.MaxEntries == 0 {
		c.Glossary.MaxEntries = 8
	}
	if len(c.Glossary.FeedURLs) == 0 {
		c.Glossary.FeedURLs = []string{
			"https://www3.nhk.or.jp/rss/news/cat0.xml",
			"https://feeds.bbci.co.uk/news/world/rss.xml",
			"https://feeds.bbci.co.uk/news/technology/rss.xml",
		}
	}

	// Subagent デフォルト
	if c.Subagent.MaxIterations == 0 {
		c.Subagent.MaxIterations = 10
	}

	if c.Security.PolicyMode == "" {
		c.Security.PolicyMode = "balanced"
	}
	if len(c.Security.DenyCommands) == 0 {
		c.Security.DenyCommands = []string{"rm -rf", "git reset --hard"}
	}
	if c.Security.Audit.Backend == "" {
		c.Security.Audit.Backend = "jsonl"
	}
	if c.Security.Audit.Path == "" {
		c.Security.Audit.Path = "logs/execution_audit.jsonl"
	}
	if c.Sandbox.Root == "" {
		c.Sandbox.Root = "sandbox"
	}
	if c.Sandbox.Storage == "" {
		c.Sandbox.Storage = "jsonl"
	}
	if c.Sandbox.SQLitePath == "" {
		workspaceDir := c.WorkspaceDir
		if workspaceDir == "" {
			workspaceDir = "./workspace"
		}
		c.Sandbox.SQLitePath = workspaceDir + "/logs/sandbox.db"
	}
	if !c.Sandbox.Promotion.RequireDiff &&
		!c.Sandbox.Promotion.RequireReason &&
		!c.Sandbox.Promotion.RequireTestResult &&
		!c.Sandbox.Promotion.RequireRollbackPlan &&
		!c.Sandbox.Promotion.RequirePostApplyVerification {
		c.Sandbox.Promotion = SandboxPromotionConfig{
			RequireDiff:                  true,
			RequireReason:                true,
			RequireTestResult:            true,
			RequireRollbackPlan:          true,
			RequirePostApplyVerification: true,
		}
	}
	if c.ViewerLog.Path == "" {
		c.ViewerLog.Path = "./workspace/orchestrator_event_log.jsonl"
	}
	if c.ViewerLog.RetentionDays <= 0 {
		c.ViewerLog.RetentionDays = 14
	}
	if c.ViewerLog.GCIntervalMinutes <= 0 {
		c.ViewerLog.GCIntervalMinutes = 60
	}
	if c.Verification.Mode == "" {
		c.Verification.Mode = "dry_run"
	}
	if c.Verification.DefaultLevel == "" {
		c.Verification.DefaultLevel = "low"
	}

	// v5.1 プロンプト/workspace デフォルト
	if c.PromptsDir == "" {
		c.PromptsDir = "./prompts"
	}
	if c.WorkspaceDir == "" {
		c.WorkspaceDir = "./workspace"
	}
	if c.Advisor.Storage == "" {
		c.Advisor.Storage = "jsonl"
	}
	if c.Advisor.LogPath == "" {
		c.Advisor.LogPath = filepath.Join(c.WorkspaceDir, "logs", "advisor")
	}
	if c.Advisor.SQLitePath == "" {
		c.Advisor.SQLitePath = filepath.Join(c.WorkspaceDir, "logs", "advisor.db")
	}
	if c.KnowledgeRelation.MaxHops == 0 {
		c.KnowledgeRelation.MaxHops = 2
	}
	if c.KnowledgeRelation.MinimumScore == 0 {
		c.KnowledgeRelation.MinimumScore = 4
	}
	if c.EconomicObjective.DraftOnly == nil {
		c.EconomicObjective.DraftOnly = boolConfigPtr(true)
	}
	if c.EconomicObjective.DailyOpportunityLimit == 0 {
		c.EconomicObjective.DailyOpportunityLimit = 5
	}
	if c.ToolHarness.Mode == "" {
		c.ToolHarness.Mode = "validate_then_repair"
	}
	if c.ToolHarness.LogPath == "" {
		c.ToolHarness.LogPath = c.WorkspaceDir + "/logs/tool_mediation.jsonl"
	}
	if c.DCI.Storage == "" {
		c.DCI.Storage = "jsonl"
	}
	if c.DCI.TracePath == "" {
		c.DCI.TracePath = c.WorkspaceDir + "/logs/dci_search_trace.jsonl"
	}
	if c.DCI.SQLitePath == "" {
		c.DCI.SQLitePath = c.WorkspaceDir + "/dci.db"
	}
	// SelfSourceDir: 未設定なら cwd を自分自身のソースディレクトリとして使う
	if c.SelfSourceDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			c.SelfSourceDir = cwd
		}
	}
	if len(c.DCI.CorpusAllowlist) == 0 {
		// 自ソースディレクトリを DCI コーパスに自動追加
		allowlist := []string{"docs/"}
		if c.SelfSourceDir != "" {
			allowlist = append(allowlist,
				filepath.Join(c.SelfSourceDir, "internal"),
				filepath.Join(c.SelfSourceDir, "cmd"),
				filepath.Join(c.SelfSourceDir, "docs"),
				filepath.Join(c.SelfSourceDir, "prompts"),
				filepath.Join(c.SelfSourceDir, "pkg"),
			)
		}
		c.DCI.CorpusAllowlist = allowlist
	}
	if len(c.DCI.CorpusDenylist) == 0 {
		c.DCI.CorpusDenylist = []string{".env", "*.pem", "*.key", "id_rsa", "credentials.json", "token.json", "cookies.sqlite", ".git", "node_modules", "venv", ".venv", "secrets", "private"}
	}
	if len(c.DCI.KnowledgeFTSDomains) == 0 {
		c.DCI.KnowledgeFTSDomains = []string{"general", "creative", "news"}
	}
	if len(c.DCI.ExplicitKeywords) == 0 {
		c.DCI.ExplicitKeywords = []string{"探して", "grep", "仕様書", "ログ", "原文", "どこに書いてある", "矛盾", "前に話した"}
	}
	if c.DCI.MaxSeconds <= 0 {
		c.DCI.MaxSeconds = 10
	}
	if c.DCI.MaxSteps <= 0 {
		c.DCI.MaxSteps = 8
	}
	if c.DCI.MaxCandidateFiles <= 0 {
		c.DCI.MaxCandidateFiles = 50
	}
	if c.DCI.MaxFilesRead <= 0 {
		c.DCI.MaxFilesRead = 10
	}
	if c.DCI.MaxEvidence <= 0 {
		c.DCI.MaxEvidence = 6
	}
	if c.DCI.MaxSnippetChars <= 0 {
		c.DCI.MaxSnippetChars = 800
	}
	if c.SkillGovernance.RegistryPath == "" {
		c.SkillGovernance.RegistryPath = c.WorkspaceDir + "/logs/skill_governance"
	}
	if c.SkillGovernance.Storage == "" {
		c.SkillGovernance.Storage = "jsonl"
	}
	if c.SkillGovernance.SQLitePath == "" {
		c.SkillGovernance.SQLitePath = c.WorkspaceDir + "/logs/skill_governance.db"
	}
	if len(c.SkillGovernance.SkillRoots) == 0 {
		c.SkillGovernance.SkillRoots = []string{"skills", "prompts/skills", "workspace/skills"}
	}
	if !c.SkillGovernance.RequiredForCoder &&
		!c.SkillGovernance.RequiredForWorker &&
		!c.SkillGovernance.WarnIfSkillNotUsed {
		c.SkillGovernance.RequiredForCoder = true
		c.SkillGovernance.RequiredForWorker = true
		c.SkillGovernance.WarnIfSkillNotUsed = true
	}
	if !c.SkillGovernance.ContributionGate.Enabled &&
		!c.SkillGovernance.ContributionGate.RequireOpenClosedPRSearch &&
		!c.SkillGovernance.ContributionGate.RequireRealProblem &&
		!c.SkillGovernance.ContributionGate.RequireCompleteDiffReview &&
		!c.SkillGovernance.ContributionGate.OneProblemPerPR {
		c.SkillGovernance.ContributionGate = SkillContributionGateConfig{
			Enabled:                   true,
			RequireOpenClosedPRSearch: true,
			RequireRealProblem:        true,
			RequireCompleteDiffReview: true,
			OneProblemPerPR:           true,
		}
	}
	if c.Workstream.LogPath == "" {
		c.Workstream.LogPath = c.WorkspaceDir + "/logs/workstream"
	}
	if c.Workstream.Storage == "" {
		c.Workstream.Storage = "jsonl"
	}
	if c.Workstream.SQLitePath == "" {
		c.Workstream.SQLitePath = c.WorkspaceDir + "/logs/workstream.db"
	}
	if c.Workstream.VaultRoot == "" {
		c.Workstream.VaultRoot = "vault/workstreams"
	}
	if !c.Workstream.RequireSuccessCriteria && !c.Workstream.RequireVerification && !c.Workstream.DraftReportOnlyHeartbeat {
		c.Workstream.RequireSuccessCriteria = true
		c.Workstream.RequireVerification = true
		c.Workstream.DraftReportOnlyHeartbeat = true
	}
	if c.Revenue.LogPath == "" {
		c.Revenue.LogPath = c.WorkspaceDir + "/logs/revenue"
	}
	if c.Revenue.Storage == "" {
		c.Revenue.Storage = "jsonl"
	}
	if c.Revenue.SQLitePath == "" {
		c.Revenue.SQLitePath = c.WorkspaceDir + "/logs/revenue.db"
	}
	if !c.Revenue.ProhibitSuccessGuarantee &&
		!c.Revenue.RequireCustomerVoicePermission {
		c.Revenue.ProhibitSuccessGuarantee = true
		c.Revenue.RequireCustomerVoicePermission = true
	}
	if c.PersonaArchitecture.LogPath == "" {
		c.PersonaArchitecture.LogPath = c.WorkspaceDir + "/logs/persona"
	}
	if c.PersonaArchitecture.Storage == "" {
		c.PersonaArchitecture.Storage = "jsonl"
	}
	if c.PersonaArchitecture.SQLitePath == "" {
		c.PersonaArchitecture.SQLitePath = c.WorkspaceDir + "/logs/persona.db"
	}
	if c.PersonaArchitecture.CharacterRoot == "" {
		c.PersonaArchitecture.CharacterRoot = c.WorkspaceDir
	}
	if c.PersonaArchitecture.TriggerCategoryPath == "" {
		c.PersonaArchitecture.TriggerCategoryPath = "triggers"
	}
	if c.PersonaArchitecture.CanonicalResponsePath == "" {
		c.PersonaArchitecture.CanonicalResponsePath = "canonical_responses"
	}
	if c.PersonaArchitecture.CanonicalResponseCooldownTurns <= 0 {
		c.PersonaArchitecture.CanonicalResponseCooldownTurns = 5
	}
	if c.PersonaArchitecture.CanonicalResponseMaxPerSession <= 0 {
		c.PersonaArchitecture.CanonicalResponseMaxPerSession = 3
	}
	if c.PersonaArchitecture.MaxTriggerCandidates <= 0 {
		c.PersonaArchitecture.MaxTriggerCandidates = 15
	}
	if !c.PersonaArchitecture.RequireLorePersonaSplit &&
		!c.PersonaArchitecture.RequireTriggerCategories &&
		!c.PersonaArchitecture.ReviewRequiredForMeta &&
		!c.PersonaArchitecture.RequireSessionKeying {
		c.PersonaArchitecture.RequireLorePersonaSplit = true
		c.PersonaArchitecture.RequireTriggerCategories = true
		c.PersonaArchitecture.ReviewRequiredForMeta = true
		c.PersonaArchitecture.RequireSessionKeying = true
	}
	if c.BrowserTraceToAPI.LogPath == "" {
		c.BrowserTraceToAPI.LogPath = c.WorkspaceDir + "/logs/browser_trace_to_api"
	}
	if c.BrowserTraceToAPI.Storage == "" {
		c.BrowserTraceToAPI.Storage = "jsonl"
	}
	if c.BrowserTraceToAPI.SQLitePath == "" {
		c.BrowserTraceToAPI.SQLitePath = c.WorkspaceDir + "/browser_trace_to_api.db"
	}
	if len(c.BrowserTraceToAPI.AcceptedPaths) == 0 {
		c.BrowserTraceToAPI.AcceptedPaths = []string{".o11y/", "traces/"}
	}
	if len(c.BrowserTraceToAPI.DenyMethods) == 0 {
		c.BrowserTraceToAPI.DenyMethods = []string{"PUT", "PATCH", "DELETE"}
	}
	if len(c.BrowserTraceToAPI.DenySensitiveFlows) == 0 {
		c.BrowserTraceToAPI.DenySensitiveFlows = []string{"payment", "purchase", "refund", "account_update", "message_send"}
	}
	if !c.BrowserTraceToAPI.ReadOnlyOnly &&
		!c.BrowserTraceToAPI.RequireTermsReview &&
		!c.BrowserTraceToAPI.GenerateOpenAPI &&
		!c.BrowserTraceToAPI.GenerateCoverageReport {
		c.BrowserTraceToAPI.ReadOnlyOnly = true
		c.BrowserTraceToAPI.RequireTermsReview = true
		c.BrowserTraceToAPI.GenerateOpenAPI = true
		c.BrowserTraceToAPI.GenerateCoverageReport = true
	}
	if c.ComplexityHotspot.LogPath == "" {
		c.ComplexityHotspot.LogPath = c.WorkspaceDir + "/logs/complexity_hotspot"
	}
	if c.ComplexityHotspot.Storage == "" {
		c.ComplexityHotspot.Storage = "jsonl"
	}
	if c.ComplexityHotspot.SQLitePath == "" {
		c.ComplexityHotspot.SQLitePath = c.WorkspaceDir + "/logs/complexity_hotspot.db"
	}
	if c.ComplexityHotspot.DefaultMode == "" {
		c.ComplexityHotspot.DefaultMode = "report_only"
	}
	if c.ComplexityHotspot.MaxHotspots <= 0 {
		c.ComplexityHotspot.MaxHotspots = 20
	}
	if len(c.ComplexityHotspot.ExcludeDirs) == 0 {
		c.ComplexityHotspot.ExcludeDirs = []string{"node_modules", ".venv", "venv", "dist", "build", "coverage", ".git"}
	}
	if !c.ComplexityHotspot.OneHotspotPerPR {
		c.ComplexityHotspot.OneHotspotPerPR = true
	}
	if c.SuperAgentHarness.LogPath == "" {
		c.SuperAgentHarness.LogPath = c.WorkspaceDir + "/logs/superagent_harness"
	}
	if c.SuperAgentHarness.Storage == "" {
		c.SuperAgentHarness.Storage = "jsonl"
	}
	if c.SuperAgentHarness.SQLitePath == "" {
		c.SuperAgentHarness.SQLitePath = c.WorkspaceDir + "/logs/superagent_harness.db"
	}
	if c.SuperAgentHarness.MaxParallelSubagents <= 0 {
		c.SuperAgentHarness.MaxParallelSubagents = 4
	}
	if c.SuperAgentHarness.MaxContextPackTokens <= 0 {
		c.SuperAgentHarness.MaxContextPackTokens = 3000
	}
	if c.SuperAgentHarness.RunQueueSchedulerIntervalSec <= 0 {
		c.SuperAgentHarness.RunQueueSchedulerIntervalSec = 60
	}
	if c.SuperAgentHarness.RunQueueSchedulerClaimLimit <= 0 {
		c.SuperAgentHarness.RunQueueSchedulerClaimLimit = 1
	}
	if !c.SuperAgentHarness.RequireScope &&
		!c.SuperAgentHarness.RequireTerminationCondition &&
		!c.SuperAgentHarness.ReturnSummaryOnly &&
		!c.SuperAgentHarness.PromotionGateRequired &&
		!c.SuperAgentHarness.TraceAgentRun {
		c.SuperAgentHarness.RequireScope = true
		c.SuperAgentHarness.RequireTerminationCondition = true
		c.SuperAgentHarness.ReturnSummaryOnly = true
		c.SuperAgentHarness.PromotionGateRequired = true
		c.SuperAgentHarness.TraceAgentRun = true
	}
	if c.AIWorkflow.LogPath == "" {
		c.AIWorkflow.LogPath = c.WorkspaceDir + "/logs/ai_workflow"
	}
	if c.AIWorkflow.Storage == "" {
		c.AIWorkflow.Storage = "jsonl"
	}
	if c.AIWorkflow.SQLitePath == "" {
		c.AIWorkflow.SQLitePath = c.WorkspaceDir + "/logs/ai_workflow.db"
	}
	if c.AIWorkflow.ProjectMemoryRoot == "" {
		c.AIWorkflow.ProjectMemoryRoot = ".ai"
	}
	if c.AIWorkflow.WorktreeBaseDir == "" {
		c.AIWorkflow.WorktreeBaseDir = "../worktrees"
	}
	if len(c.AIWorkflow.RequiredCLITools) == 0 {
		c.AIWorkflow.RequiredCLITools = []string{"rg", "fd", "jq", "git"}
	}
	if len(c.AIWorkflow.ExternalControlAllowedActors) == 0 {
		c.AIWorkflow.ExternalControlAllowedActors = []string{"Worker", "Coder", "external-client"}
	}
	if len(c.AIWorkflow.ExternalControlAllowedChannels) == 0 {
		c.AIWorkflow.ExternalControlAllowedChannels = []string{"local", "viewer", "mobile"}
	}
	if len(c.AIWorkflow.ExternalControlAllowedActions) == 0 {
		c.AIWorkflow.ExternalControlAllowedActions = []string{"promotion_request", "promotion_apply", "promotion_rollback", "artifact_review", "status_read"}
	}
	if c.AIWorkflow.ContextBudgetWarnRatio <= 0 {
		c.AIWorkflow.ContextBudgetWarnRatio = 0.8
	}
	if c.AIWorkflow.ContextBudgetStopRatio <= 0 {
		c.AIWorkflow.ContextBudgetStopRatio = 0.95
	}
	if c.AIWorkflow.HeavyWorkerFileThreshold <= 0 {
		c.AIWorkflow.HeavyWorkerFileThreshold = 20
	}
	if c.AIWorkflow.HeavyWorkerSpecThreshold <= 0 {
		c.AIWorkflow.HeavyWorkerSpecThreshold = 1
	}
	if c.AIWorkflow.HeavyWorkerRetryThreshold <= 0 {
		c.AIWorkflow.HeavyWorkerRetryThreshold = 2
	}
	if !c.AIWorkflow.RequiredBeforeModify &&
		!c.AIWorkflow.WorktreeRequiredForWrite &&
		!c.AIWorkflow.ContextTrackingEnabled {
		c.AIWorkflow.RequiredBeforeModify = true
		c.AIWorkflow.WorktreeRequiredForWrite = true
		c.AIWorkflow.ContextTrackingEnabled = true
	}
	if c.KnowledgeMemory.LogPath == "" {
		c.KnowledgeMemory.LogPath = c.WorkspaceDir + "/logs/knowledge_memory"
	}
	if c.KnowledgeMemory.Storage == "" {
		c.KnowledgeMemory.Storage = "jsonl"
	}
	if c.KnowledgeMemory.SQLitePath == "" {
		c.KnowledgeMemory.SQLitePath = c.WorkspaceDir + "/logs/knowledge_memory.db"
	}
	if !c.KnowledgeMemory.ProtectPersonalArchive &&
		!c.KnowledgeMemory.DreamRequiresReview &&
		!c.KnowledgeMemory.DailyIntakePromoteToStaging {
		c.KnowledgeMemory.ProtectPersonalArchive = true
		c.KnowledgeMemory.DreamRequiresReview = true
		c.KnowledgeMemory.DailyIntakePromoteToStaging = true
	}
	if c.Storage.Memory.SessionDir == "" {
		c.Storage.Memory.SessionDir = "./data/sessions"
	}
	c.Session.StorageDir = c.Storage.Memory.SessionDir
	if c.Storage.Memory.OperationMemoryDir == "" {
		c.Storage.Memory.OperationMemoryDir = DefaultOperationMemoryDir()
	}
	c.OperationMemoryDir = c.Storage.Memory.OperationMemoryDir
	if c.Verification.ReportPath == "" {
		c.Verification.ReportPath = c.WorkspaceDir + "/verification_report.jsonl"
	}
	if !c.ViewerLog.Enabled {
		c.ViewerLog.Enabled = true
	}
	if c.TTS.OutputDir == "" {
		c.TTS.OutputDir = "./workspace/tts"
	}
	if c.TTS.GatewayBaseURL == "" {
		c.TTS.GatewayBaseURL = "http://127.0.0.1:7870"
	}
	if shouldEnableLocalTLSSkipVerify(c.TTS.GatewayBaseURL) {
		c.TTS.TLSSkipVerify = true
	}
	if c.TTS.TimeoutMS <= 0 {
		c.TTS.TimeoutMS = 120000
	}
	if c.TTS.VoiceID == "" {
		c.TTS.VoiceID = "mio"
	}
	if c.TTS.Speed <= 0 {
		c.TTS.Speed = 1.2
	}
	if c.TTS.PronunciationCheck.ToolBaseURL == "" {
		c.TTS.PronunciationCheck.ToolBaseURL = "http://127.0.0.1:7892"
	}
	if c.TTS.PronunciationCheck.Schedule == "" {
		c.TTS.PronunciationCheck.Schedule = "cron 30 19 * * *"
	}
	if c.TTS.PronunciationCheck.GPUMatch == "" {
		c.TTS.PronunciationCheck.GPUMatch = "RTX 5060 Ti"
	}
	if c.TTS.PronunciationCheck.MinFreeMB <= 0 {
		c.TTS.PronunciationCheck.MinFreeMB = 768
	}
	if c.TTS.PronunciationCheck.MaxUtilizationPercent <= 0 {
		c.TTS.PronunciationCheck.MaxUtilizationPercent = 10
	}
	if c.TTS.PronunciationCheck.IdleSamples <= 0 {
		c.TTS.PronunciationCheck.IdleSamples = 5
	}
	if c.TTS.PronunciationCheck.SampleIntervalSeconds <= 0 {
		c.TTS.PronunciationCheck.SampleIntervalSeconds = 2
	}
	if c.TTS.PronunciationCheck.RetryIntervalSeconds <= 0 {
		c.TTS.PronunciationCheck.RetryIntervalSeconds = 300
	}
	if c.TTS.PronunciationCheck.TimeoutMinutes <= 0 {
		c.TTS.PronunciationCheck.TimeoutMinutes = 45
	}
	if strings.TrimSpace(c.STT.GatewayBaseURL) == "" {
		c.STT.GatewayBaseURL = "http://127.0.0.1:8766"
	}
	if c.STT.TimeoutMS <= 0 {
		c.STT.TimeoutMS = 8000
	}
	if c.STT.BusyPolicy == "" {
		c.STT.BusyPolicy = "queue_latest"
	}
	if c.STT.EndpointPath == "" {
		c.STT.EndpointPath = "/stt"
	}
	if strings.TrimSpace(c.Vision.BaseURL) == "" {
		c.Vision.BaseURL = "http://127.0.0.1:8770"
	}
	if c.Vision.TimeoutMS <= 0 {
		c.Vision.TimeoutMS = 120000
	}
	if c.Vision.MaxImageBytes <= 0 {
		c.Vision.MaxImageBytes = 20 << 20
	}
	if c.Vision.MaxVideoBytes <= 0 {
		c.Vision.MaxVideoBytes = 100 << 20
	}
	if c.Vision.MaxFrames <= 0 {
		c.Vision.MaxFrames = 8
	}
	if strings.TrimSpace(c.Image.BaseURL) == "" {
		c.Image.BaseURL = "http://127.0.0.1:8780"
	}
	if c.Image.TimeoutMS <= 0 {
		c.Image.TimeoutMS = 600000
	}
	if c.VTuber.TickIntervalMS <= 0 {
		c.VTuber.TickIntervalMS = 100
	}
	if c.VTuber.ConnectTimeout <= 0 {
		c.VTuber.ConnectTimeout = 3000
	}
	if c.VTuber.WriteTimeout <= 0 {
		c.VTuber.WriteTimeout = 2000
	}
	if c.AudioRouter.ConnectTimeoutMS <= 0 {
		c.AudioRouter.ConnectTimeoutMS = 5000
	}
	if c.AudioRouter.DownloadTimeoutMS <= 0 {
		c.AudioRouter.DownloadTimeoutMS = 15000
	}
	if c.AudioRouter.RetryDelayMS <= 0 {
		c.AudioRouter.RetryDelayMS = 2000
	}
	if c.AudioRouter.BufferMS <= 0 {
		c.AudioRouter.BufferMS = 120
	}

	// Coder スロットのデフォルト値（v4.1）
	if c.Coder1.Name == "" {
		c.Coder1.Name = "aka"
	}
	if c.Coder1.DisplayName == "" {
		c.Coder1.DisplayName = "赤"
	}
	if c.Coder1.LightMemory.MaxTurns == 0 {
		c.Coder1.LightMemory.MaxTurns = 3
	}

	if c.Coder2.Name == "" {
		c.Coder2.Name = "ao"
	}
	if c.Coder2.DisplayName == "" {
		c.Coder2.DisplayName = "青"
	}
	if c.Coder2.LightMemory.MaxTurns == 0 {
		c.Coder2.LightMemory.MaxTurns = 3
	}

	if c.Coder3.Name == "" {
		c.Coder3.Name = "kin"
	}
	if c.Coder3.DisplayName == "" {
		c.Coder3.DisplayName = "金"
	}
	if c.Coder3.LightMemory.MaxTurns == 0 {
		c.Coder3.LightMemory.MaxTurns = 3
	}

	if c.Coder4.Name == "" {
		c.Coder4.Name = "gin"
	}
	if c.Coder4.DisplayName == "" {
		c.Coder4.DisplayName = "銀"
	}
	if c.Coder4.LightMemory.MaxTurns == 0 {
		c.Coder4.LightMemory.MaxTurns = 3
	}
}

func (c *Config) applyIdleChatEpisodePreparationDefaults() {
	p := &c.IdleChat.EpisodePreparation
	if p.Enabled == nil {
		enabled := true
		p.Enabled = &enabled
	}
	if p.Generator == "" {
		p.Generator = "codex_exe"
	}
	if p.ReadyTarget == 0 {
		p.ReadyTarget = 3
	}
	if p.NoReadyBehavior == "" {
		p.NoReadyBehavior = "preparing"
	}
	if p.MaxSuffixRegenerations == 0 {
		p.MaxSuffixRegenerations = 3
	}
	if p.CodexExe.Sandbox == "" {
		p.CodexExe.Sandbox = "read-only"
	}
	if p.CodexExe.Ephemeral == nil {
		ephemeral := true
		p.CodexExe.Ephemeral = &ephemeral
	}
	tts := &c.IdleChat.TTSPrefetch
	if tts.InitialUtterances == 0 {
		tts.InitialUtterances = 2
	}
	if tts.LookaheadUtterances == 0 {
		tts.LookaheadUtterances = 3
	}
	if tts.LowWatermarkSeconds == 0 {
		tts.LowWatermarkSeconds = 15
	}
	if tts.TargetBufferSeconds == 0 {
		tts.TargetBufferSeconds = 30
	}
}

func boolConfigPtr(value bool) *bool {
	return &value
}

func (c *Config) applyIdleChatNewsSourceDefaults() {
	if c.IdleChat.NewsSources.Reddit.Enabled == nil {
		c.IdleChat.NewsSources.Reddit.Enabled = boolConfigPtr(true)
	}
	if len(c.IdleChat.NewsSources.Reddit.Communities) == 0 {
		c.IdleChat.NewsSources.Reddit.Communities = []string{"technology", "worldnews", "science", "economics"}
	}
	if c.IdleChat.NewsSources.Reddit.Limit <= 0 {
		c.IdleChat.NewsSources.Reddit.Limit = 8
	}
	if strings.TrimSpace(c.IdleChat.NewsSources.X.BearerTokenEnv) == "" {
		c.IdleChat.NewsSources.X.BearerTokenEnv = "RENCROW_X_BEARER_TOKEN"
	}
	if len(c.IdleChat.NewsSources.X.Queries) == 0 {
		c.IdleChat.NewsSources.X.Queries = []IdleChatXNewsQueryConfig{
			{
				Name:     "X Japan Trends",
				Category: "social",
				Query:    "(ニュース OR 速報 OR 話題) lang:ja -is:retweet",
				Limit:    10,
			},
		}
	}
}

func (c *Config) applyIdleChatSpeakerLLMDefaults() {
	if c.IdleChat.SpeakerLLMOptions == nil {
		c.IdleChat.SpeakerLLMOptions = make(map[string]IdleChatLLMOptions)
	}
	for _, participant := range c.IdleChat.Participants {
		name := strings.ToLower(strings.TrimSpace(participant))
		if name == "" {
			continue
		}
		opts := c.IdleChat.SpeakerLLMOptions[name]
		if opts.Think == nil {
			think := name != "mio" && name != "shiro"
			opts.Think = &think
		}
		c.IdleChat.SpeakerLLMOptions[name] = opts
	}
}

func (c *Config) applyIdleChatTopicGenerationDefaults() {
	tg := &c.IdleChat.TopicGeneration
	if !tg.Enabled {
		tg.Enabled = true
	}
	if tg.CandidatesPerAttempt == 0 {
		tg.CandidatesPerAttempt = 5
	}
	if tg.MaxAttempts == 0 {
		tg.MaxAttempts = 3
	}
	if !tg.JudgeEnabled {
		tg.JudgeEnabled = true
	}
	if tg.MinJudgeTotal == 0 {
		tg.MinJudgeTotal = 24
	}
	if tg.MinCategoryFit == 0 {
		tg.MinCategoryFit = 4
	}
	if tg.MinSafety == 0 {
		tg.MinSafety = 4
	}
	if tg.RecentTopicWindow == 0 {
		tg.RecentTopicWindow = 12
	}
	if tg.RecentSimilarityThreshold == 0 {
		tg.RecentSimilarityThreshold = 0.82
	}
	if !tg.LogCandidates {
		tg.LogCandidates = true
	}
	if !tg.LogJudgeScores {
		tg.LogJudgeScores = true
	}
	if tg.Prompts.Common == "" {
		tg.Prompts.Common = "prompts/idle_chat/topic_generator_common.md"
	}
	if tg.Prompts.Single == "" {
		tg.Prompts.Single = "prompts/idle_chat/topic_generator_single.md"
	}
	if tg.Prompts.Double == "" {
		tg.Prompts.Double = "prompts/idle_chat/topic_generator_double.md"
	}
	if tg.Prompts.External == "" {
		tg.Prompts.External = "prompts/idle_chat/topic_generator_external.md"
	}
	if tg.Prompts.Movie == "" {
		tg.Prompts.Movie = "prompts/idle_chat/topic_generator_movie.md"
	}
	if tg.Prompts.News == "" {
		tg.Prompts.News = "prompts/idle_chat/topic_generator_news.md"
	}
	if tg.Prompts.Forecast == "" {
		tg.Prompts.Forecast = "prompts/idle_chat/topic_generator_forecast.md"
	}
	if tg.Prompts.Story == "" {
		tg.Prompts.Story = "prompts/idle_chat/topic_generator_story.md"
	}
	if tg.Prompts.Judge == "" {
		tg.Prompts.Judge = "prompts/idle_chat/topic_judge.md"
	}
}

func (c *Config) applyIdleChatDialogueInterestingnessDefaults() {
	d := &c.IdleChat.DialogueInterestingness
	if !d.Enabled {
		d.Enabled = true
	}
	if d.MaxTurnsPerTopic == 0 {
		d.MaxTurnsPerTopic = 12
	}
	if d.MinQualityScore == 0 {
		d.MinQualityScore = 70
	}
	if d.MaxQualityRetries == 0 {
		d.MaxQualityRetries = 4
	}
	if !d.EnforcePreviousUptake {
		d.EnforcePreviousUptake = true
	}
	if !d.EnforceOneNewContribution {
		d.EnforceOneNewContribution = true
	}
	if !d.EnforceCategoryAxis {
		d.EnforceCategoryAxis = true
	}
	if !d.ForbidMetaLeak {
		d.ForbidMetaLeak = true
	}
	if !d.ForbidUserQuestion {
		d.ForbidUserQuestion = true
	}
	if d.Utterance.MinRunes == 0 {
		d.Utterance.MinRunes = 20
	}
	if d.Utterance.MaxRunes == 0 {
		d.Utterance.MaxRunes = 160
	}
	if d.Utterance.PreferredMaxSentences == 0 {
		d.Utterance.PreferredMaxSentences = 2
	}
	if d.Prompts.Common == "" {
		d.Prompts.Common = "prompts/idle_chat/dialogue_common.md"
	}
	if d.Prompts.Single == "" {
		d.Prompts.Single = "prompts/idle_chat/dialogue_single.md"
	}
	if d.Prompts.Double == "" {
		d.Prompts.Double = "prompts/idle_chat/dialogue_double.md"
	}
	if d.Prompts.External == "" {
		d.Prompts.External = "prompts/idle_chat/dialogue_external.md"
	}
	if d.Prompts.Movie == "" {
		d.Prompts.Movie = "prompts/idle_chat/dialogue_movie.md"
	}
	if d.Prompts.News == "" {
		d.Prompts.News = "prompts/idle_chat/dialogue_news.md"
	}
	if d.Prompts.Forecast == "" {
		d.Prompts.Forecast = "prompts/idle_chat/dialogue_forecast.md"
	}
	if d.Prompts.Story == "" {
		d.Prompts.Story = "prompts/idle_chat/dialogue_story.md"
	}
}

func defaultRenCrowToolsPath(parts ...string) string {
	root := strings.TrimSpace(os.Getenv("RENCROW_TOOLS_ROOT"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			root = filepath.Join(home, "RenCrow", "RenCrow_Tools")
		}
	}
	if root == "" {
		root = filepath.Join("RenCrow", "RenCrow_Tools")
	}
	return filepath.Join(append([]string{root}, parts...)...)
}

// DefaultOperationMemoryDir returns the runtime-owned operation memory directory.
func DefaultOperationMemoryDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		return filepath.Join(".rencrow", "memory")
	}
	return filepath.Join(homeDir, ".rencrow", "memory")
}
