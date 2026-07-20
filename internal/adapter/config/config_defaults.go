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

	if c.Ollama.Model == "" {
		c.Ollama.Model = "rencrow-v1"
	}

	if c.Claude.Model == "" {
		c.Claude.Model = "claude-sonnet-4-20250514"
	}

	if c.DeepSeek.Model == "" {
		c.DeepSeek.Model = "deepseek-chat"
	}

	if c.OpenAI.Model == "" {
		c.OpenAI.Model = "gpt-4o-mini"
	}

	if c.LocalLLM.Provider == "" {
		c.LocalLLM.Provider = "local_openai"
	}
	if c.LocalLLM.ChatModel == "" {
		c.LocalLLM.ChatModel = "Chat"
	}
	if c.LocalLLM.WorkerModel == "" {
		c.LocalLLM.WorkerModel = "Worker"
	}
	if c.LocalLLM.ChatWorkerModel == "" {
		c.LocalLLM.ChatWorkerModel = "ChatWorker"
	}
	if c.LocalLLM.HeavyModel == "" {
		c.LocalLLM.HeavyModel = "Heavy"
	}
	if c.LocalLLM.WildModel == "" {
		c.LocalLLM.WildModel = "Wild"
	}
	if c.LocalLLM.TimeoutSec <= 0 {
		c.LocalLLM.TimeoutSec = 120
	}
	if c.LocalLLM.Warmup == nil {
		v := true
		c.LocalLLM.Warmup = &v
	}
	if c.LocalLLM.GlobalConcurrency <= 0 {
		c.LocalLLM.GlobalConcurrency = 2
	}
	if c.LocalLLM.ModelConcurrency <= 0 {
		c.LocalLLM.ModelConcurrency = 1
	}
	if c.Ollama.MaxContext <= 0 {
		c.Ollama.MaxContext = 131072
	}
	if c.LocalLLM.ModelContext <= 0 {
		c.LocalLLM.ModelContext = 131072
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
	if c.WebwrightFetch.ResponsesEndpoint == "" {
		workerBase := strings.TrimRight(strings.TrimSpace(c.LocalLLM.WorkerBaseURL), "/")
		if workerBase == "" {
			workerBase = strings.TrimRight(strings.TrimSpace(c.LocalLLM.BaseURL), "/")
		}
		if workerBase == "" {
			workerBase = "http://127.0.0.1:8082"
		}
		c.WebwrightFetch.ResponsesEndpoint = workerBase + "/v1/responses"
	}
	if strings.TrimSpace(c.ComfyUI.BaseURL) == "" {
		c.ComfyUI.BaseURL = "http://100.83.207.6:8188"
	}
	if strings.TrimSpace(c.ComfyUI.ClientID) == "" {
		c.ComfyUI.ClientID = "rencrow-server"
	}
	if c.ComfyUI.PollIntervalSec <= 0 {
		c.ComfyUI.PollIntervalSec = 3
	}
	if c.ComfyUI.TimeoutSec <= 0 {
		c.ComfyUI.TimeoutSec = 300
	}
	if c.WebwrightFetch.Model == "" {
		c.WebwrightFetch.Model = "Coder1"
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
	}

	// v5.0 Conversation デフォルト
	// enabled: false がデフォルト（明示的に有効化が必要）
	if c.Conversation.RedisURL == "" {
		c.Conversation.RedisURL = "redis://localhost:6379"
	}
	if c.Conversation.ArchiveSQLitePath == "" {
		if c.Conversation.DeprecatedArchivePath != "" {
			c.Conversation.ArchiveSQLitePath = archiveSQLitePathFromLegacy(c.Conversation.DeprecatedArchivePath)
		} else {
			c.Conversation.ArchiveSQLitePath = "/var/lib/rencrow/memory_archive.db"
		}
	}
	if c.Conversation.VectorDBURL == "" {
		c.Conversation.VectorDBURL = "localhost:6334"
	}

	// Heartbeat デフォルト
	if c.Heartbeat.Interval == 0 {
		c.Heartbeat.Interval = 30
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
		!c.Sandbox.Promotion.RequireHumanApproval &&
		!c.Sandbox.Promotion.RequirePostApplyVerification {
		c.Sandbox.Promotion = SandboxPromotionConfig{
			RequireDiff:                  true,
			RequireReason:                true,
			RequireTestResult:            true,
			RequireRollbackPlan:          true,
			RequireHumanApproval:         true,
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
		!c.SkillGovernance.ContributionGate.RequireHumanApproval &&
		!c.SkillGovernance.ContributionGate.OneProblemPerPR {
		c.SkillGovernance.ContributionGate = SkillContributionGateConfig{
			Enabled:                   true,
			RequireOpenClosedPRSearch: true,
			RequireRealProblem:        true,
			RequireCompleteDiffReview: true,
			RequireHumanApproval:      true,
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
		!c.Revenue.RequireCustomerVoicePermission &&
		!c.Revenue.ExternalPublishRequiresApproval &&
		!c.Revenue.HighTicketOfferRequiresApproval {
		c.Revenue.ProhibitSuccessGuarantee = true
		c.Revenue.RequireCustomerVoicePermission = true
		c.Revenue.ExternalPublishRequiresApproval = true
		c.Revenue.HighTicketOfferRequiresApproval = true
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
		!c.PersonaArchitecture.HumanReviewRequiredForMeta &&
		!c.PersonaArchitecture.RequireSessionKeying {
		c.PersonaArchitecture.RequireLorePersonaSplit = true
		c.PersonaArchitecture.RequireTriggerCategories = true
		c.PersonaArchitecture.HumanReviewRequiredForMeta = true
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
		!c.BrowserTraceToAPI.RequireHumanApprovalPromote &&
		!c.BrowserTraceToAPI.GenerateOpenAPI &&
		!c.BrowserTraceToAPI.GenerateCoverageReport {
		c.BrowserTraceToAPI.ReadOnlyOnly = true
		c.BrowserTraceToAPI.RequireTermsReview = true
		c.BrowserTraceToAPI.RequireHumanApprovalPromote = true
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
	if !c.ComplexityHotspot.RequireHumanApprovalForPatch && !c.ComplexityHotspot.OneHotspotPerPR {
		c.ComplexityHotspot.RequireHumanApprovalForPatch = true
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
		!c.SuperAgentHarness.ExternalSendRequiresApproval &&
		!c.SuperAgentHarness.TraceAgentRun {
		c.SuperAgentHarness.RequireScope = true
		c.SuperAgentHarness.RequireTerminationCondition = true
		c.SuperAgentHarness.ReturnSummaryOnly = true
		c.SuperAgentHarness.PromotionGateRequired = true
		c.SuperAgentHarness.ExternalSendRequiresApproval = true
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
	if len(c.AIWorkflow.ExternalControlApprovalRequired) == 0 {
		c.AIWorkflow.ExternalControlApprovalRequired = []string{"promotion_apply", "promotion_rollback", "external_send"}
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
		!c.KnowledgeMemory.DreamRequiresHumanReview &&
		!c.KnowledgeMemory.DailyIntakePromoteToStaging {
		c.KnowledgeMemory.ProtectPersonalArchive = true
		c.KnowledgeMemory.DreamRequiresHumanReview = true
		c.KnowledgeMemory.DailyIntakePromoteToStaging = true
	}
	if c.OperationMemoryDir == "" {
		c.OperationMemoryDir = DefaultOperationMemoryDir()
	}
	if c.Verification.ReportPath == "" {
		c.Verification.ReportPath = c.WorkspaceDir + "/verification_report.jsonl"
	}
	if !c.ViewerLog.Enabled {
		c.ViewerLog.Enabled = true
	}
	if len(c.TTS.ProviderPriority) == 0 {
		c.TTS.ProviderPriority = []string{"irodori"}
	}
	if c.TTS.OutputDir == "" {
		c.TTS.OutputDir = "./workspace/tts"
	}
	if c.TTS.HTTPBaseURL == "" {
		c.TTS.HTTPBaseURL = "https://127.0.0.1:8770"
		c.TTS.TLSSkipVerify = true
	}
	if shouldEnableLocalTLSSkipVerify(c.TTS.HTTPBaseURL) {
		c.TTS.TLSSkipVerify = true
	}
	if c.TTS.TimeoutMS <= 0 {
		c.TTS.TimeoutMS = 15000
	}
	if c.TTS.VoiceID == "" {
		c.TTS.VoiceID = "mio"
	}
	if c.TTS.Speed <= 0 {
		c.TTS.Speed = 1.2
	}
	if c.TTS.Irodori.VoiceID == "" {
		c.TTS.Irodori.VoiceID = c.TTS.VoiceID
	}
	if c.TTS.Irodori.VoiceName == "" && (strings.EqualFold(c.TTS.Irodori.VoiceID, "mio") || strings.EqualFold(c.TTS.Irodori.VoiceID, "female_01")) {
		c.TTS.Irodori.VoiceName = "Mio"
	}
	if c.TTS.Irodori.VoiceName == "" && (strings.EqualFold(c.TTS.Irodori.VoiceID, "shiro") || strings.EqualFold(c.TTS.Irodori.VoiceID, "male_01")) {
		c.TTS.Irodori.VoiceName = "Shiro"
	}
	if c.TTS.Irodori.EndpointPath == "" {
		c.TTS.Irodori.EndpointPath = "/api/tts"
	}
	if c.TTS.Irodori.TimeoutSec <= 0 {
		c.TTS.Irodori.TimeoutSec = 120
	}
	if c.TTS.Irodori.Checkpoint == "" {
		c.TTS.Irodori.Checkpoint = "Aratako/Irodori-TTS-500M-v2"
	}
	if c.TTS.Irodori.ModelDevice == "" {
		c.TTS.Irodori.ModelDevice = "mps"
	}
	if c.TTS.Irodori.ModelPrecision == "" {
		c.TTS.Irodori.ModelPrecision = "fp32"
	}
	if c.TTS.Irodori.CodecDevice == "" {
		c.TTS.Irodori.CodecDevice = "mps"
	}
	if c.TTS.Irodori.CodecPrecision == "" {
		c.TTS.Irodori.CodecPrecision = "fp32"
	}
	if c.TTS.Irodori.NumSteps <= 0 {
		c.TTS.Irodori.NumSteps = 16
	}
	if c.TTS.Irodori.NumCandidates <= 0 {
		c.TTS.Irodori.NumCandidates = 1
	}
	if c.TTS.Irodori.CFGGuidanceMode == "" {
		c.TTS.Irodori.CFGGuidanceMode = "independent"
	}
	if c.TTS.Irodori.CFGScaleText == 0 {
		c.TTS.Irodori.CFGScaleText = 3.0
	}
	if c.TTS.Irodori.CFGScaleSpeaker == 0 {
		c.TTS.Irodori.CFGScaleSpeaker = 5.0
	}
	if c.TTS.Irodori.CFGMinT == 0 {
		c.TTS.Irodori.CFGMinT = 0.5
	}
	if c.TTS.Irodori.CFGMaxT == 0 {
		c.TTS.Irodori.CFGMaxT = 1.0
	}
	if !c.TTS.Irodori.ContextKVCache {
		c.TTS.Irodori.ContextKVCache = true
	}
	if c.TTS.ProviderParams == nil {
		c.TTS.ProviderParams = map[string]any{}
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
	if c.STT.Provider == "" {
		c.STT.Provider = "external_http"
	}
	if c.STT.Language == "" {
		c.STT.Language = "ja"
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
	if c.STT.ProviderParams == nil {
		c.STT.ProviderParams = map[string]any{}
	}
	if envURL := strings.TrimSpace(os.Getenv("STT_PROVIDER_URL")); envURL != "" && c.STT.ProviderURL == "" && c.STT.ExternalHTTP.URL == "" {
		c.STT.Provider = "external_http"
		c.STT.ProviderURL = envURL
	}
	if c.STT.ProviderURL == "" {
		c.STT.ProviderURL = c.STT.ExternalHTTP.URL
	}
	if c.STT.StreamURL == "" {
		c.STT.StreamURL = c.STT.ExternalHTTP.StreamURL
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
	if c.Coder1.Provider == "" {
		c.Coder1.Provider = "deepseek"
	}
	if c.Coder1.Model == "" {
		c.Coder1.Model = "deepseek-coder"
	}
	if c.Coder1.Name == "" {
		c.Coder1.Name = "ao"
	}
	if c.Coder1.DisplayName == "" {
		c.Coder1.DisplayName = "青"
	}
	if c.Coder1.LightMemory.MaxTurns == 0 {
		c.Coder1.LightMemory.MaxTurns = 3
	}

	if c.Coder2.Provider == "" {
		c.Coder2.Provider = "openai"
	}
	if c.Coder2.Model == "" {
		c.Coder2.Model = "gpt-4-turbo"
	}
	if c.Coder2.Name == "" {
		c.Coder2.Name = "aka"
	}
	if c.Coder2.DisplayName == "" {
		c.Coder2.DisplayName = "赤"
	}
	if c.Coder2.LightMemory.MaxTurns == 0 {
		c.Coder2.LightMemory.MaxTurns = 3
	}

	if c.Coder3.Provider == "" {
		c.Coder3.Provider = "claude"
	}
	if c.Coder3.Model == "" {
		c.Coder3.Model = "claude-sonnet-4"
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

	if c.Coder4.Provider == "" {
		c.Coder4.Provider = "gemini"
	}
	if c.Coder4.Model == "" {
		c.Coder4.Model = "gemini-2.0-flash-exp"
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

func archiveSQLitePathFromLegacy(legacyPath string) string {
	legacyPath = strings.TrimSpace(legacyPath)
	if legacyPath == "" || legacyPath == ":memory:" {
		return legacyPath
	}
	cleaned := filepath.Clean(legacyPath)
	if strings.EqualFold(filepath.Ext(cleaned), ".duckdb") {
		return filepath.ToSlash(strings.TrimSuffix(cleaned, filepath.Ext(cleaned)) + "_archive.db")
	}
	return filepath.ToSlash(cleaned)
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
