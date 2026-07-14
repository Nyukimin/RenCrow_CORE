package config

import (
	"fmt"
	"log"
	"strings"
)

// Validate は設定の妥当性を検証
func (c *Config) Validate() error {
	// サーバー設定検証
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d (must be 1-65535)", c.Server.Port)
	}

	if c.LocalLLM.Enabled {
		if c.LocalLLM.Provider != "local_openai" && c.LocalLLM.Provider != "ollama" {
			return fmt.Errorf("local_llm.provider must be one of [local_openai, ollama], got '%s'", c.LocalLLM.Provider)
		}
		if c.LocalLLM.BaseURL == "" && c.LocalLLM.ChatBaseURL == "" && c.LocalLLM.WorkerBaseURL == "" && c.LocalLLM.HeavyBaseURL == "" && c.LocalLLM.WildBaseURL == "" {
			return fmt.Errorf("local_llm base_url or role-specific base_url is required when enabled=true")
		}
		if c.LocalLLM.ChatModel == "" {
			return fmt.Errorf("local_llm chat_model is required when enabled=true")
		}
		if c.LocalLLM.WorkerModel == "" {
			return fmt.Errorf("local_llm worker_model is required when enabled=true")
		}
		if c.LocalLLM.HeavyModel == "" {
			return fmt.Errorf("local_llm heavy_model is required when enabled=true")
		}
		if c.LocalLLM.WildModel == "" {
			return fmt.Errorf("local_llm wild_model is required when enabled=true")
		}
		if c.LocalLLM.TimeoutSec < 1 {
			return fmt.Errorf("local_llm timeout_sec must be >= 1")
		}
		if c.LocalLLM.GlobalConcurrency < 1 {
			return fmt.Errorf("local_llm global_concurrency must be >= 1")
		}
		if c.LocalLLM.ModelConcurrency < 1 {
			return fmt.Errorf("local_llm model_concurrency must be >= 1")
		}
	}

	if c.LLMOps.Enabled {
		if strings.TrimSpace(c.LLMOps.BaseURL) == "" {
			return fmt.Errorf("llm_ops.base_url is required when llm_ops.enabled=true")
		}
	}
	if c.WebwrightFetch.Enabled {
		if strings.TrimSpace(c.WebwrightFetch.RunnerPath) == "" {
			return fmt.Errorf("webwright_fetch.runner_path is required when webwright_fetch.enabled=true")
		}
		if strings.TrimSpace(c.WebwrightFetch.ConfigPath) == "" {
			return fmt.Errorf("webwright_fetch.config_path is required when webwright_fetch.enabled=true")
		}
		if strings.TrimSpace(c.WebwrightFetch.OutputDir) == "" {
			return fmt.Errorf("webwright_fetch.output_dir is required when webwright_fetch.enabled=true")
		}
		if strings.TrimSpace(c.WebwrightFetch.ResponsesEndpoint) == "" {
			return fmt.Errorf("webwright_fetch.responses_endpoint is required when webwright_fetch.enabled=true")
		}
		if strings.TrimSpace(c.WebwrightFetch.Model) == "" {
			return fmt.Errorf("webwright_fetch.model is required when webwright_fetch.enabled=true")
		}
	}
	if strings.TrimSpace(c.WebGather.SearXNGBaseURL) != "" {
		base := strings.TrimSpace(c.WebGather.SearXNGBaseURL)
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			return fmt.Errorf("web_gather.searxng_base_url must start with http:// or https://")
		}
	}
	if c.BrowserActor.Enabled {
		if strings.TrimSpace(c.BrowserActor.RunnerPath) == "" {
			return fmt.Errorf("browser_actor.runner_path is required when browser_actor.enabled=true")
		}
		if strings.TrimSpace(c.BrowserActor.NodeBinary) == "" {
			return fmt.Errorf("browser_actor.node_binary is required when browser_actor.enabled=true")
		}
		if strings.TrimSpace(c.BrowserActor.ArtifactRoot) == "" {
			return fmt.Errorf("browser_actor.artifact_root is required when browser_actor.enabled=true")
		}
		if c.BrowserActor.TimeoutMS < 1 {
			return fmt.Errorf("browser_actor.timeout_ms must be >= 1")
		}
		if c.BrowserActor.MaxActions < 1 || c.BrowserActor.MaxActions > 100 {
			return fmt.Errorf("browser_actor.max_actions must be between 1 and 100")
		}
	}
	if strings.TrimSpace(c.BrowserActor.NetworkScope) != "" && c.BrowserActor.NetworkScope != "allowlist" && c.BrowserActor.NetworkScope != "blocked" {
		return fmt.Errorf("browser_actor.network_scope must be one of [allowlist, blocked]")
	}
	for _, p := range []string{c.BrowserActor.ProfileRoot, c.BrowserActor.ArtifactRoot} {
		if strings.Contains(p, "..") {
			return fmt.Errorf("browser_actor paths must not contain '..'")
		}
	}
	if c.Codex.Enabled {
		if strings.TrimSpace(c.Codex.Command) == "" {
			return fmt.Errorf("codex.command is required when codex.enabled=true")
		}
		if c.Codex.Sandbox != "read-only" && c.Codex.Sandbox != "workspace-write" {
			return fmt.Errorf("codex.sandbox must be one of [read-only, workspace-write]")
		}
		if c.Codex.TimeoutMS < 1000 || c.Codex.TimeoutMS > 3600000 {
			return fmt.Errorf("codex.timeout_ms must be between 1000 and 3600000")
		}
		if c.Codex.MaxPromptBytes < 1 || c.Codex.MaxPromptBytes > 1048576 {
			return fmt.Errorf("codex.max_prompt_bytes must be between 1 and 1048576")
		}
		if c.Codex.MaxOutputBytes < 1024 || c.Codex.MaxOutputBytes > 10485760 {
			return fmt.Errorf("codex.max_output_bytes must be between 1024 and 10485760")
		}
		if strings.Contains(c.Codex.WorkingDir, "..") {
			return fmt.Errorf("codex.working_dir must not contain '..'")
		}
	}
	advisorConfigured := strings.TrimSpace(c.Advisor.Storage) != "" || strings.TrimSpace(c.Advisor.LogPath) != "" || strings.TrimSpace(c.Advisor.SQLitePath) != ""
	if advisorConfigured {
		if c.Advisor.Storage != "jsonl" && c.Advisor.Storage != "sqlite" {
			return fmt.Errorf("advisor.storage must be jsonl or sqlite")
		}
		if strings.TrimSpace(c.Advisor.LogPath) == "" {
			return fmt.Errorf("advisor.log_path is required")
		}
		if c.Advisor.Storage == "sqlite" && strings.TrimSpace(c.Advisor.SQLitePath) == "" {
			return fmt.Errorf("advisor.sqlite_path is required when advisor.storage=sqlite")
		}
	}
	knowledgeRelationConfigured := c.KnowledgeRelation.Enabled || c.KnowledgeRelation.BuildOnImport || c.KnowledgeRelation.MaxHops != 0 || c.KnowledgeRelation.MinimumScore != 0
	if knowledgeRelationConfigured {
		if c.KnowledgeRelation.MaxHops < 1 || c.KnowledgeRelation.MaxHops > 2 {
			return fmt.Errorf("knowledge_relation.max_hops must be 1 or 2")
		}
		if c.KnowledgeRelation.MinimumScore <= 0 {
			return fmt.Errorf("knowledge_relation.minimum_score must be > 0")
		}
		if c.KnowledgeRelation.BuildOnImport && !c.KnowledgeRelation.Enabled {
			return fmt.Errorf("knowledge_relation.build_on_import requires enabled=true")
		}
	}

	if !c.LocalLLM.Enabled {
		// Ollama設定検証
		if c.Ollama.BaseURL == "" {
			return fmt.Errorf("ollama base_url is required")
		}

		if c.Ollama.Model == "" {
			return fmt.Errorf("ollama model is required")
		}
	}

	// セッション設定検証
	if c.Session.StorageDir == "" {
		return fmt.Errorf("session storage_dir is required")
	}

	// LINE設定検証（片方だけ設定は警告）
	hasSecret := c.Line.ChannelSecret != ""
	hasToken := c.Line.AccessToken != ""
	if hasSecret != hasToken {
		log.Println("WARN: LINE config incomplete - both channel_secret and access_token are required for webhook")
	}

	if c.VTuber.Enabled {
		if c.VTuber.TickIntervalMS < 50 || c.VTuber.TickIntervalMS > 100 {
			return fmt.Errorf("vtuber.tick_interval_ms must be between 50 and 100, got %d", c.VTuber.TickIntervalMS)
		}
		if len(c.VTuber.Characters) == 0 {
			return fmt.Errorf("vtuber.enabled=true requires at least one character")
		}
		for name, ch := range c.VTuber.Characters {
			if ch.AudioOutput == "" {
				return fmt.Errorf("vtuber.characters.%s.audio_output is required", name)
			}
			if ch.VTSHost == "" {
				return fmt.Errorf("vtuber.characters.%s.vts_host is required", name)
			}
			if ch.VTSPort < 1 || ch.VTSPort > 65535 {
				return fmt.Errorf("vtuber.characters.%s.vts_port must be 1-65535, got %d", name, ch.VTSPort)
			}
			if len(ch.ExpressionMap) == 0 {
				return fmt.Errorf("vtuber.characters.%s.expression_map is required", name)
			}
		}
	}

	if c.AudioRouter.Enabled {
		if c.AudioRouter.SSEURL == "" {
			return fmt.Errorf("audio_router.sse_url is required when audio_router.enabled=true")
		}
		if len(c.AudioRouter.DeviceMap) == 0 {
			return fmt.Errorf("audio_router.device_map is required when audio_router.enabled=true")
		}
		for name, dev := range c.AudioRouter.DeviceMap {
			if dev.DeviceID == "" {
				return fmt.Errorf("audio_router.device_map.%s.device_id is required", name)
			}
		}
		if c.AudioRouter.ConnectTimeoutMS < 1000 {
			return fmt.Errorf("audio_router.connect_timeout_ms must be >= 1000")
		}
		if c.AudioRouter.DownloadTimeoutMS < 1000 {
			return fmt.Errorf("audio_router.download_timeout_ms must be >= 1000")
		}
		if c.AudioRouter.RetryDelayMS < 250 {
			return fmt.Errorf("audio_router.retry_delay_ms must be >= 250")
		}
		if c.AudioRouter.BufferMS < 20 || c.AudioRouter.BufferMS > 5000 {
			return fmt.Errorf("audio_router.buffer_ms must be between 20 and 5000")
		}
	}

	// v4.0 Distributed設定検証
	if c.Distributed.Enabled {
		if len(c.Distributed.Transports) == 0 {
			return fmt.Errorf("distributed.enabled=true requires at least one transport")
		}
		for name, tc := range c.Distributed.Transports {
			if tc.Type != "local" && tc.Type != "ssh" {
				return fmt.Errorf("distributed.transports.%s.type must be 'local' or 'ssh', got '%s'", name, tc.Type)
			}
			if tc.Type == "ssh" {
				if tc.RemoteHost == "" {
					return fmt.Errorf("distributed.transports.%s.remote_host is required for ssh type", name)
				}
				if tc.RemoteUser == "" {
					return fmt.Errorf("distributed.transports.%s.remote_user is required for ssh type", name)
				}
				if tc.SSHKeyPath == "" {
					return fmt.Errorf("distributed.transports.%s.ssh_key_path is required for ssh type", name)
				}
			}
		}
	}

	if c.Worker.LightMemory.Enabled && (c.Worker.LightMemory.MaxTurns < 1 || c.Worker.LightMemory.MaxTurns > 20) {
		return fmt.Errorf("worker.light_memory.max_turns must be between 1 and 20, got %d", c.Worker.LightMemory.MaxTurns)
	}

	// v4.0 IdleChat設定検証
	if c.IdleChat.Enabled {
		validAgents := map[string]bool{
			"mio": true, "shiro": true, "aka": true, "ao": true, "gin": true,
		}
		for _, p := range c.IdleChat.Participants {
			if !validAgents[p] {
				return fmt.Errorf("idle_chat.participants: unknown agent '%s'", p)
			}
		}
		effectiveIntervalSec := c.IdleChat.IntervalSec
		if effectiveIntervalSec == 0 {
			effectiveIntervalSec = c.IdleChat.IntervalMin * 60
		}
		if effectiveIntervalSec < 1 {
			return fmt.Errorf("idle_chat.interval_sec must be >= 1")
		}
		if c.IdleChat.MaxTurns < 1 || c.IdleChat.MaxTurns > 100 {
			return fmt.Errorf("idle_chat.max_turns must be between 1 and 100")
		}
		if c.IdleChat.Temperature < 0 || c.IdleChat.Temperature > 2.0 {
			return fmt.Errorf("idle_chat.temperature must be between 0 and 2.0")
		}
		if c.IdleChat.TopicGeneration.Enabled {
			tg := c.IdleChat.TopicGeneration
			if tg.CandidatesPerAttempt < 1 {
				return fmt.Errorf("idle_chat.topic_generation.candidates_per_attempt must be >= 1")
			}
			if tg.MaxAttempts < 1 {
				return fmt.Errorf("idle_chat.topic_generation.max_attempts must be >= 1")
			}
			if tg.RecentSimilarityThreshold < 0 || tg.RecentSimilarityThreshold > 1 {
				return fmt.Errorf("idle_chat.topic_generation.recent_similarity_threshold must be between 0 and 1")
			}
		}
		if c.IdleChat.DialogueInterestingness.Enabled {
			d := c.IdleChat.DialogueInterestingness
			if d.MaxTurnsPerTopic < 1 {
				return fmt.Errorf("idle_chat.dialogue_interestingness.max_turns_per_topic must be >= 1")
			}
			if d.MinQualityScore < 0 || d.MinQualityScore > 100 {
				return fmt.Errorf("idle_chat.dialogue_interestingness.min_quality_score must be between 0 and 100")
			}
			if d.MaxQualityRetries < 0 {
				return fmt.Errorf("idle_chat.dialogue_interestingness.max_quality_retries must be >= 0")
			}
			if d.Utterance.MinRunes < 0 || d.Utterance.MaxRunes < 1 || d.Utterance.MinRunes > d.Utterance.MaxRunes {
				return fmt.Errorf("idle_chat.dialogue_interestingness.utterance rune bounds are invalid")
			}
		}
	}

	// v5.0 Conversation設定検証
	if c.Conversation.Enabled {
		if c.Conversation.RedisURL == "" {
			return fmt.Errorf("conversation.redis_url is required when conversation.enabled=true")
		}
		if c.Conversation.DuckDBPath == "" {
			return fmt.Errorf("conversation.duckdb_path is required when conversation.enabled=true")
		}
		if c.Conversation.VectorDBURL == "" {
			return fmt.Errorf("conversation.vectordb_url is required when conversation.enabled=true")
		}
	}

	if c.Security.Enabled {
		if c.Security.PolicyMode != "strict" && c.Security.PolicyMode != "balanced" && c.Security.PolicyMode != "dev" {
			return fmt.Errorf("security.policy_mode must be 'strict', 'balanced', or 'dev'")
		}
		if c.Security.NetworkScope != "" &&
			c.Security.NetworkScope != "blocked" &&
			c.Security.NetworkScope != "allowlist" &&
			c.Security.NetworkScope != "full" {
			return fmt.Errorf("security.network_scope must be 'blocked', 'allowlist', or 'full'")
		}
		if c.Security.Audit.Backend != "jsonl" && c.Security.Audit.Backend != "sqlite" {
			return fmt.Errorf("security.audit.backend must be 'jsonl' or 'sqlite'")
		}
	}
	if c.ToolHarness.Mode != "" &&
		c.ToolHarness.Mode != "validate_then_repair" &&
		c.ToolHarness.Mode != "log_only" &&
		c.ToolHarness.Mode != "strict" {
		return fmt.Errorf("tool_harness.mode must be 'validate_then_repair', 'log_only', or 'strict'")
	}
	if c.Sandbox.Enabled || c.Sandbox.DenyOutsideSandboxWrite {
		if strings.TrimSpace(c.Sandbox.Root) == "" {
			return fmt.Errorf("sandbox.root is required when sandbox is enabled")
		}
		if c.Sandbox.Storage != "jsonl" && c.Sandbox.Storage != "sqlite" {
			return fmt.Errorf("sandbox.storage must be jsonl or sqlite")
		}
		if c.Sandbox.Storage == "sqlite" && strings.TrimSpace(c.Sandbox.SQLitePath) == "" {
			return fmt.Errorf("sandbox.sqlite_path is required when sandbox.storage is sqlite")
		}
		if c.Sandbox.DenyOutsideSandboxWrite && !c.Security.Enabled {
			return fmt.Errorf("security.enabled is required when sandbox.deny_outside_sandbox_write=true")
		}
	}
	if c.DCI.IsEnabled() {
		if c.DCI.Storage != "" && c.DCI.Storage != "jsonl" && c.DCI.Storage != "sqlite" {
			return fmt.Errorf("dci.storage must be jsonl or sqlite")
		}
		if c.DCI.MaxFilesRead < 0 {
			return fmt.Errorf("dci.max_files_read must be >= 1")
		}
		if c.DCI.MaxEvidence < 0 {
			return fmt.Errorf("dci.max_evidence must be >= 1")
		}
		if c.DCI.MaxSeconds < 0 {
			return fmt.Errorf("dci.max_seconds must be >= 1")
		}
		if c.DCI.MaxSteps < 0 {
			return fmt.Errorf("dci.max_steps must be >= 1")
		}
		if c.DCI.MaxCandidateFiles < 0 {
			return fmt.Errorf("dci.max_candidate_files must be >= 1")
		}
		if c.DCI.MaxSnippetChars < 0 {
			return fmt.Errorf("dci.max_snippet_chars must be >= 1")
		}
		for _, domain := range c.DCI.KnowledgeFTSDomains {
			domain = strings.TrimSpace(domain)
			if domain == "" {
				continue
			}
			if strings.ContainsAny(domain, " \t\r\n:") {
				return fmt.Errorf("invalid dci.knowledge_fts_domains value: %s", domain)
			}
		}
		if c.DCI.Storage != "sqlite" && c.DCI.Enabled != nil && *c.DCI.Enabled && strings.TrimSpace(c.DCI.TracePath) == "" {
			return fmt.Errorf("dci.trace_path is required when dci.enabled=true")
		}
		if c.DCI.Storage == "sqlite" && strings.TrimSpace(c.DCI.SQLitePath) == "" {
			return fmt.Errorf("dci.sqlite_path is required when dci.storage=sqlite")
		}
	}
	if (c.SkillGovernance.Enabled != nil && *c.SkillGovernance.Enabled) ||
		strings.TrimSpace(c.SkillGovernance.RegistryPath) != "" ||
		len(c.SkillGovernance.SkillRoots) > 0 {
		if strings.TrimSpace(c.SkillGovernance.RegistryPath) == "" {
			return fmt.Errorf("skill_governance.registry_path is required when skill_governance is enabled")
		}
		if c.SkillGovernance.Storage != "jsonl" && c.SkillGovernance.Storage != "sqlite" {
			return fmt.Errorf("skill_governance.storage must be jsonl or sqlite")
		}
		if c.SkillGovernance.Storage == "sqlite" && strings.TrimSpace(c.SkillGovernance.SQLitePath) == "" {
			return fmt.Errorf("skill_governance.sqlite_path is required when skill_governance.storage is sqlite")
		}
		if len(c.SkillGovernance.SkillRoots) == 0 {
			return fmt.Errorf("skill_governance.skill_roots is required when skill_governance is enabled")
		}
	}
	if (c.Workstream.Enabled != nil && *c.Workstream.Enabled) ||
		strings.TrimSpace(c.Workstream.LogPath) != "" ||
		strings.TrimSpace(c.Workstream.VaultRoot) != "" {
		if strings.TrimSpace(c.Workstream.LogPath) == "" {
			return fmt.Errorf("workstream.log_path is required when workstream is enabled")
		}
		if c.Workstream.Storage != "jsonl" && c.Workstream.Storage != "sqlite" {
			return fmt.Errorf("workstream.storage must be jsonl or sqlite")
		}
		if c.Workstream.Storage == "sqlite" && strings.TrimSpace(c.Workstream.SQLitePath) == "" {
			return fmt.Errorf("workstream.sqlite_path is required when workstream.storage is sqlite")
		}
		if strings.TrimSpace(c.Workstream.VaultRoot) == "" {
			return fmt.Errorf("workstream.vault_root is required when workstream is enabled")
		}
	}
	if (c.Revenue.Enabled != nil && *c.Revenue.Enabled) ||
		strings.TrimSpace(c.Revenue.LogPath) != "" {
		if strings.TrimSpace(c.Revenue.LogPath) == "" {
			return fmt.Errorf("revenue.log_path is required when revenue is enabled")
		}
		if c.Revenue.Storage != "jsonl" && c.Revenue.Storage != "sqlite" {
			return fmt.Errorf("revenue.storage must be jsonl or sqlite")
		}
		if c.Revenue.Storage == "sqlite" && strings.TrimSpace(c.Revenue.SQLitePath) == "" {
			return fmt.Errorf("revenue.sqlite_path is required when revenue.storage is sqlite")
		}
	}
	if (c.PersonaArchitecture.Enabled != nil && *c.PersonaArchitecture.Enabled) ||
		strings.TrimSpace(c.PersonaArchitecture.LogPath) != "" {
		if strings.TrimSpace(c.PersonaArchitecture.LogPath) == "" {
			return fmt.Errorf("persona_architecture.log_path is required when persona_architecture is enabled")
		}
		if c.PersonaArchitecture.Storage != "jsonl" && c.PersonaArchitecture.Storage != "sqlite" {
			return fmt.Errorf("persona_architecture.storage must be jsonl or sqlite")
		}
		if c.PersonaArchitecture.Storage == "sqlite" && strings.TrimSpace(c.PersonaArchitecture.SQLitePath) == "" {
			return fmt.Errorf("persona_architecture.sqlite_path is required when persona_architecture.storage is sqlite")
		}
		if strings.TrimSpace(c.PersonaArchitecture.CharacterRoot) == "" {
			return fmt.Errorf("persona_architecture.character_root is required when persona_architecture is enabled")
		}
	}
	if c.PersonaArchitecture.MaxTriggerCandidates < 0 {
		return fmt.Errorf("persona_architecture.max_trigger_candidates must be >= 0")
	}
	if c.PersonaArchitecture.CanonicalResponseCooldownTurns < 0 {
		return fmt.Errorf("persona_architecture.canonical_response_cooldown_turns must be >= 0")
	}
	if c.PersonaArchitecture.CanonicalResponseMaxPerSession < 0 {
		return fmt.Errorf("persona_architecture.canonical_response_max_per_session must be >= 0")
	}
	if (c.BrowserTraceToAPI.Enabled != nil && *c.BrowserTraceToAPI.Enabled) ||
		strings.TrimSpace(c.BrowserTraceToAPI.LogPath) != "" {
		if c.BrowserTraceToAPI.Storage != "" && c.BrowserTraceToAPI.Storage != "jsonl" && c.BrowserTraceToAPI.Storage != "sqlite" {
			return fmt.Errorf("browser_trace_to_api.storage must be jsonl or sqlite")
		}
		if strings.TrimSpace(c.BrowserTraceToAPI.LogPath) == "" {
			return fmt.Errorf("browser_trace_to_api.log_path is required when browser_trace_to_api is enabled")
		}
		if c.BrowserTraceToAPI.Storage == "sqlite" && strings.TrimSpace(c.BrowserTraceToAPI.SQLitePath) == "" {
			return fmt.Errorf("browser_trace_to_api.sqlite_path is required when browser_trace_to_api.storage=sqlite")
		}
	}
	for _, method := range c.BrowserTraceToAPI.DenyMethods {
		switch strings.ToUpper(strings.TrimSpace(method)) {
		case "", "GET", "HEAD", "OPTIONS":
			return fmt.Errorf("browser_trace_to_api.deny_methods must not include safe or empty method")
		}
	}
	for _, path := range c.BrowserTraceToAPI.AcceptedPaths {
		path = strings.TrimSpace(path)
		if path == "" {
			return fmt.Errorf("browser_trace_to_api.accepted_paths must not include empty path")
		}
		if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "~") {
			return fmt.Errorf("browser_trace_to_api.accepted_paths must be relative safe paths")
		}
	}
	for _, flow := range c.BrowserTraceToAPI.DenySensitiveFlows {
		if strings.TrimSpace(flow) == "" {
			return fmt.Errorf("browser_trace_to_api.deny_sensitive_flows must not include empty flow")
		}
	}
	if (c.ComplexityHotspot.Enabled != nil && *c.ComplexityHotspot.Enabled) ||
		strings.TrimSpace(c.ComplexityHotspot.LogPath) != "" {
		if strings.TrimSpace(c.ComplexityHotspot.LogPath) == "" {
			return fmt.Errorf("complexity_hotspot.log_path is required when complexity_hotspot is enabled")
		}
	}
	if c.ComplexityHotspot.Storage != "" && c.ComplexityHotspot.Storage != "jsonl" && c.ComplexityHotspot.Storage != "sqlite" {
		return fmt.Errorf("complexity_hotspot.storage must be jsonl or sqlite")
	}
	if c.ComplexityHotspot.Storage == "sqlite" && strings.TrimSpace(c.ComplexityHotspot.SQLitePath) == "" {
		return fmt.Errorf("complexity_hotspot.sqlite_path is required when complexity_hotspot.storage=sqlite")
	}
	switch strings.TrimSpace(c.ComplexityHotspot.DefaultMode) {
	case "", "report_only":
	default:
		return fmt.Errorf("complexity_hotspot.default_mode must be report_only")
	}
	if c.ComplexityHotspot.MaxHotspots < 0 {
		return fmt.Errorf("complexity_hotspot.max_hotspots must be >= 0")
	}
	if c.ComplexityHotspot.AutoApply {
		return fmt.Errorf("complexity_hotspot.auto_apply must be false")
	}
	superAgentConfigured := (c.SuperAgentHarness.Enabled != nil && *c.SuperAgentHarness.Enabled) ||
		strings.TrimSpace(c.SuperAgentHarness.LogPath) != ""
	if superAgentConfigured {
		if strings.TrimSpace(c.SuperAgentHarness.LogPath) == "" {
			return fmt.Errorf("superagent_harness.log_path is required when superagent_harness is enabled")
		}
	}
	if c.SuperAgentHarness.Storage != "" && c.SuperAgentHarness.Storage != "jsonl" && c.SuperAgentHarness.Storage != "sqlite" {
		return fmt.Errorf("superagent_harness.storage must be jsonl or sqlite")
	}
	if c.SuperAgentHarness.Storage == "sqlite" && strings.TrimSpace(c.SuperAgentHarness.SQLitePath) == "" {
		return fmt.Errorf("superagent_harness.sqlite_path is required when superagent_harness.storage=sqlite")
	}
	if c.SuperAgentHarness.MaxParallelSubagents < 0 {
		return fmt.Errorf("superagent_harness.max_parallel_subagents must be >= 0")
	}
	if c.SuperAgentHarness.MaxContextPackTokens < 0 {
		return fmt.Errorf("superagent_harness.max_context_pack_tokens must be >= 0")
	}
	if c.SuperAgentHarness.RunQueueSchedulerIntervalSec < 0 {
		return fmt.Errorf("superagent_harness.run_queue_scheduler_interval_sec must be >= 0")
	}
	if c.SuperAgentHarness.RunQueueSchedulerClaimLimit < 0 {
		return fmt.Errorf("superagent_harness.run_queue_scheduler_claim_limit must be >= 0")
	}
	if superAgentConfigured && !c.SuperAgentHarness.TraceAgentRun {
		return fmt.Errorf("superagent_harness.trace_agent_run must be true when superagent_harness is enabled")
	}
	if superAgentConfigured && !c.SuperAgentHarness.ReturnSummaryOnly {
		return fmt.Errorf("superagent_harness.return_summary_only must be true when superagent_harness is enabled")
	}
	aiWorkflowConfigured := (c.AIWorkflow.Enabled != nil && *c.AIWorkflow.Enabled) ||
		strings.TrimSpace(c.AIWorkflow.LogPath) != "" ||
		strings.TrimSpace(c.AIWorkflow.SQLitePath) != ""
	if aiWorkflowConfigured {
		if strings.TrimSpace(c.AIWorkflow.LogPath) == "" {
			return fmt.Errorf("ai_workflow.log_path is required when ai_workflow is enabled")
		}
		if strings.TrimSpace(c.AIWorkflow.ProjectMemoryRoot) == "" {
			return fmt.Errorf("ai_workflow.project_memory_root is required when ai_workflow is enabled")
		}
		if strings.TrimSpace(c.AIWorkflow.WorktreeBaseDir) == "" {
			return fmt.Errorf("ai_workflow.worktree_base_dir is required when ai_workflow is enabled")
		}
	}
	if c.AIWorkflow.Storage != "" && c.AIWorkflow.Storage != "jsonl" && c.AIWorkflow.Storage != "sqlite" {
		return fmt.Errorf("ai_workflow.storage must be jsonl or sqlite")
	}
	if c.AIWorkflow.Storage == "sqlite" && strings.TrimSpace(c.AIWorkflow.SQLitePath) == "" {
		return fmt.Errorf("ai_workflow.sqlite_path is required when ai_workflow.storage=sqlite")
	}
	if c.AIWorkflow.ContextBudgetTokens < 0 {
		return fmt.Errorf("ai_workflow.context_budget_tokens must be >= 0")
	}
	if c.AIWorkflow.ContextBudgetWarnRatio < 0 || c.AIWorkflow.ContextBudgetWarnRatio > 1 {
		return fmt.Errorf("ai_workflow.context_budget_warn_ratio must be between 0 and 1")
	}
	if c.AIWorkflow.ContextBudgetStopRatio < 0 || c.AIWorkflow.ContextBudgetStopRatio > 1 {
		return fmt.Errorf("ai_workflow.context_budget_stop_ratio must be between 0 and 1")
	}
	if c.AIWorkflow.ContextBudgetWarnRatio > 0 &&
		c.AIWorkflow.ContextBudgetStopRatio > 0 &&
		c.AIWorkflow.ContextBudgetStopRatio < c.AIWorkflow.ContextBudgetWarnRatio {
		return fmt.Errorf("ai_workflow.context_budget_stop_ratio must be >= context_budget_warn_ratio")
	}
	if c.AIWorkflow.HeavyWorkerFileThreshold < 0 {
		return fmt.Errorf("ai_workflow.heavy_worker_file_threshold must be >= 0")
	}
	if c.AIWorkflow.HeavyWorkerSpecThreshold < 0 {
		return fmt.Errorf("ai_workflow.heavy_worker_spec_threshold must be >= 0")
	}
	if c.AIWorkflow.HeavyWorkerRetryThreshold < 0 {
		return fmt.Errorf("ai_workflow.heavy_worker_retry_threshold must be >= 0")
	}
	if err := validateNonEmptyList("ai_workflow.external_control_allowed_actors", c.AIWorkflow.ExternalControlAllowedActors); err != nil {
		return err
	}
	if err := validateNonEmptyList("ai_workflow.external_control_allowed_channels", c.AIWorkflow.ExternalControlAllowedChannels); err != nil {
		return err
	}
	if err := validateNonEmptyList("ai_workflow.external_control_allowed_actions", c.AIWorkflow.ExternalControlAllowedActions); err != nil {
		return err
	}
	if err := validateNonEmptyList("ai_workflow.external_control_approval_required", c.AIWorkflow.ExternalControlApprovalRequired); err != nil {
		return err
	}
	knowledgeMemoryConfigured := (c.KnowledgeMemory.Enabled != nil && *c.KnowledgeMemory.Enabled) ||
		strings.TrimSpace(c.KnowledgeMemory.LogPath) != ""
	if knowledgeMemoryConfigured {
		if strings.TrimSpace(c.KnowledgeMemory.LogPath) == "" {
			return fmt.Errorf("knowledge_memory.log_path is required when knowledge_memory is enabled")
		}
		if c.KnowledgeMemory.Storage != "jsonl" && c.KnowledgeMemory.Storage != "sqlite" {
			return fmt.Errorf("knowledge_memory.storage must be jsonl or sqlite")
		}
		if c.KnowledgeMemory.Storage == "sqlite" && strings.TrimSpace(c.KnowledgeMemory.SQLitePath) == "" {
			return fmt.Errorf("knowledge_memory.sqlite_path is required when knowledge_memory.storage is sqlite")
		}
		if !c.KnowledgeMemory.ProtectPersonalArchive {
			return fmt.Errorf("knowledge_memory.protect_personal_archive must be true when knowledge_memory is enabled")
		}
		if !c.KnowledgeMemory.DreamRequiresHumanReview {
			return fmt.Errorf("knowledge_memory.dream_requires_human_review must be true when knowledge_memory is enabled")
		}
	}
	if c.ViewerLog.Enabled {
		if c.ViewerLog.RetentionDays < 1 {
			return fmt.Errorf("viewer_log.retention_days must be >= 1")
		}
		if c.ViewerLog.GCIntervalMinutes < 1 {
			return fmt.Errorf("viewer_log.gc_interval_minutes must be >= 1")
		}
		if c.ViewerLog.Path == "" {
			return fmt.Errorf("viewer_log.path is required when viewer_log.enabled=true")
		}
	}

	// v4.1 Coder スロット検証
	coders := []struct {
		name   string
		config *CoderConfig
	}{
		{"coder1", &c.Coder1},
		{"coder2", &c.Coder2},
		{"coder3", &c.Coder3},
		{"coder4", &c.Coder4},
	}

	for _, coder := range coders {
		if err := validateCoderConfig(coder.name, coder.config); err != nil {
			return err
		}
	}

	return nil
}

// validateCoderConfig は単一 CoderConfig の妥当性を検証
func validateCoderConfig(name string, cc *CoderConfig) error {
	// Provider 検証
	validProviders := map[string]bool{
		"deepseek":     true,
		"openai":       true,
		"claude":       true,
		"gemini":       true,
		"ollama":       true,
		"local_openai": true,
	}
	if cc.Provider != "" && !validProviders[cc.Provider] {
		return fmt.Errorf("%s.provider must be one of [deepseek, openai, claude, gemini, ollama, local_openai], got '%s'", name, cc.Provider)
	}

	// Model 検証（Enabled=true の場合のみ必須）
	if cc.Enabled && cc.Model == "" {
		return fmt.Errorf("%s.model is required when enabled=true", name)
	}

	// Name 検証（識別子として使用されるため常に必須）
	if cc.Name == "" {
		return fmt.Errorf("%s.name is required", name)
	}

	// DisplayName 検証（UI表示用、空でも許容するがログで警告）
	if cc.DisplayName == "" {
		log.Printf("WARN: %s.display_name is empty, using name '%s' for display", name, cc.Name)
	}

	// LightMemory.MaxTurns 検証
	if cc.LightMemory.Enabled && (cc.LightMemory.MaxTurns < 1 || cc.LightMemory.MaxTurns > 20) {
		return fmt.Errorf("%s.light_memory.max_turns must be between 1 and 20, got %d", name, cc.LightMemory.MaxTurns)
	}

	// APIKey/BaseURL 検証（provider 別、Enabled=true の場合のみ）
	if cc.Enabled {
		switch cc.Provider {
		case "deepseek", "openai", "claude", "gemini":
			if cc.APIKey == "" {
				return fmt.Errorf("%s.api_key is required for provider '%s' when enabled=true", name, cc.Provider)
			}
		case "ollama", "local_openai":
			if cc.BaseURL == "" {
				return fmt.Errorf("%s.base_url is required for provider '%s' when enabled=true", name, cc.Provider)
			}
		}
	}

	return nil
}

func validateNonEmptyList(name string, values []string) error {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] must not be empty", name, i)
		}
	}
	return nil
}
