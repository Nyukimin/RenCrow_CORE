package config

// Config はアプリケーション全体の設定
// v3既存フィールドをそのまま維持し、v4.0で Distributed, IdleChat を追加
type Config struct {
	// === v3.0 既存フィールド ===
	Server   ServerConfig   `yaml:"server"`
	Ollama   OllamaConfig   `yaml:"ollama"`
	Claude   ClaudeConfig   `yaml:"claude"`
	DeepSeek DeepSeekConfig `yaml:"deepseek"`
	OpenAI   OpenAIConfig   `yaml:"openai"`
	Session  SessionConfig  `yaml:"session"`
	Worker   WorkerConfig   `yaml:"worker"`
	Line     LineConfig     `yaml:"line"`
	Telegram TelegramConfig `yaml:"telegram"`
	Discord  DiscordConfig  `yaml:"discord"`
	Slack    SlackConfig    `yaml:"slack"`
	Log      LogConfig      `yaml:"log"`

	// === v4.0 追加フィールド ===
	Distributed DistributedConfig `yaml:"distributed"`
	IdleChat    IdleChatConfig    `yaml:"idle_chat"`

	// === v5.0 追加フィールド ===
	Conversation ConversationConfig `yaml:"conversation"`

	// === MLX / local OpenAI-compatible LLM runtime ===
	LocalLLM LocalLLMConfig `yaml:"local_llm"`

	// === Runtime module placement source of truth ===
	RuntimeTopology RuntimeTopologyConfig `yaml:"runtime_topology"`

	// === Optional Webwright browser-backed fetch bridge ===
	WebwrightFetch WebwrightFetchConfig `yaml:"webwright_fetch"`

	// === Web Gather public web collection tool ===
	WebGather WebGatherConfig `yaml:"web_gather"`

	// === Browser Actor user-like browser operation sidecar ===
	BrowserActor BrowserActorConfig `yaml:"browser_actor"`

	// === Codex non-interactive coding agent bridge ===
	Codex CodexConfig `yaml:"codex"`

	// === ComfyUI image generation backend ===
	ComfyUI ComfyUIConfig `yaml:"comfyui"`

	// === v5.1 プロンプト外部ファイル ===
	PromptsDir         string         `yaml:"prompts_dir"`          // プロンプトファイルのベースディレクトリ（デフォルト）
	WorkspaceDir       string         `yaml:"workspace_dir"`        // ユーザーカスタマイズ領域（オーバーライド）
	OperationMemoryDir string         `yaml:"operation_memory_dir"` // RenCrow operational memory の永続ディレクトリ
	SelfSourceDir      string         `yaml:"self_source_dir"`      // RenCrow 自身のソースコードディレクトリ（デフォルト: cwd）
	Prompts            *LoadedPrompts `yaml:"-"`                    // 読み込み済みプロンプト（YAML非対象）

	// === Heartbeat ===
	Heartbeat HeartbeatConfig `yaml:"heartbeat"`

	// === Glossary / Recent Topics ===
	Glossary GlossaryConfig `yaml:"glossary"`

	// === Google Search API ===
	GoogleSearchChat   GoogleSearchConfig `yaml:"google_search_chat"`
	GoogleSearchWorker GoogleSearchConfig `yaml:"google_search_worker"`

	// === Subagent ===
	Subagent SubagentConfig `yaml:"subagent"`

	// === Capability Detection (v4.1) ===
	Capability CapabilityConfig `yaml:"capability"`

	// === Security / Execution Audit ===
	Security SecurityConfig `yaml:"security"`

	// === Sandbox / Promotion Gate ===
	Sandbox SandboxConfig `yaml:"sandbox"`

	// === Tool Harness Contract Mediation ===
	ToolHarness ToolHarnessConfig `yaml:"tool_harness"`

	// === DCI / Direct Corpus Interaction ===
	DCI DCIConfig `yaml:"dci"`

	// === Skill Governance ===
	SkillGovernance SkillGovernanceConfig `yaml:"skill_governance"`

	// === Workstream Operating Loop ===
	Workstream WorkstreamConfig `yaml:"workstream"`

	// === Revenue Operating Workflow ===
	Revenue RevenueConfig `yaml:"revenue"`

	// === Persona Lore / Mutual Observation ===
	PersonaArchitecture PersonaArchitectureConfig `yaml:"persona_architecture"`

	// === Browser Trace to API Discovery ===
	BrowserTraceToAPI BrowserTraceToAPIConfig `yaml:"browser_trace_to_api"`

	// === Codebase Complexity Hotspot Skill ===
	ComplexityHotspot ComplexityHotspotConfig `yaml:"complexity_hotspot"`

	// === SuperAgent Harness ===
	SuperAgentHarness SuperAgentHarnessConfig `yaml:"superagent_harness"`

	// === AI Native Engineering Workflow ===
	AIWorkflow AIWorkflowConfig `yaml:"ai_workflow"`

	// === Knowledge Memory Extension ===
	KnowledgeMemory KnowledgeMemoryConfig `yaml:"knowledge_memory"`

	// === TTS / OpenClaw parity ===
	TTS TTSConfig `yaml:"tts"`

	// === STT / HTTPS Viewer voice input ===
	STT STTConfig `yaml:"stt"`

	// === VTuber / VTube Studio integration ===
	VTuber VTuberConfig `yaml:"vtuber"`

	// === Coder4 AudioRouter ===
	AudioRouter AudioRouterConfig `yaml:"audio_router"`

	// === Viewer persisted JSON operation log ===
	ViewerLog ViewerLogConfig `yaml:"viewer_log"`

	// === Response verification pipeline ===
	Verification VerificationConfig `yaml:"verification"`

	// === Viewer → MLX 管理デーモン プロキシ（stop / restart / status）===
	// トークンは環境変数 LLM_OPS_TOKEN のみ（YAML に平文保存しないこと）。
	LLMOps LLMOpsConfig `yaml:"llm_ops"`

	// === Agent Persona files (v4.2) ===
	MioPersonaFile string `yaml:"mio_persona_file"` // workspace_dir からの相対パス

	// === Coder スロット（v4.1: 4体化 + Agent Persona） ===
	Coder1 CoderConfig `yaml:"coder1"`
	Coder2 CoderConfig `yaml:"coder2"`
	Coder3 CoderConfig `yaml:"coder3"`
	Coder4 CoderConfig `yaml:"coder4"` // 新規追加
}

// SessionConfig はセッション設定
type SessionConfig struct {
	StorageDir string `yaml:"storage_dir"`
}

// WorkerConfig はWorker実行設定
type WorkerConfig struct {
	// === v3.0 既存フィールド ===
	AutoCommit           bool     `yaml:"auto_commit"`
	CommitMessagePrefix  string   `yaml:"commit_message_prefix"`
	CommandTimeout       int      `yaml:"command_timeout"` // 秒
	GitTimeout           int      `yaml:"git_timeout"`     // 秒
	StopOnError          bool     `yaml:"stop_on_error"`
	Workspace            string   `yaml:"workspace"`
	ProtectedPatterns    []string `yaml:"protected_patterns"`
	ActionOnProtected    string   `yaml:"action_on_protected"` // "error", "skip", "log"
	ShowExecutionSummary bool     `yaml:"show_execution_summary"`

	// === v4.0 追加フィールド ===
	ParallelExecution bool `yaml:"parallel_execution"` // true で並列実行（デフォルト: false）
	MaxParallelism    int  `yaml:"max_parallelism"`    // 並列度上限（デフォルト: 4）

	// === v4.2: Agent Persona ===
	PersonaFile string            `yaml:"persona_file"` // workspace_dir からの相対パス
	Tone        string            `yaml:"tone"`         // 口調ヒント（TTS 連携用）
	LightMemory LightMemoryConfig `yaml:"light_memory"` // Shiro のインメモリ短期記憶

	// === v4.1: Autonomous ===
	MaxRepair int `yaml:"max_repair"` // 自律実行のリペア上限（0以下は1とみなす、デフォルト: 1）
}

// LineConfig はLINE Messaging API設定
type LineConfig struct {
	ChannelSecret string              `yaml:"channel_secret"` // 環境変数 LINE_CHANNEL_SECRET 推奨
	AccessToken   string              `yaml:"access_token"`   // 環境変数 LINE_CHANNEL_TOKEN 推奨
	ChannelPolicy ChannelPolicyConfig `yaml:"channel_policy"`
}

type ChannelPolicyConfig struct {
	Enabled        bool     `yaml:"enabled"`
	AllowDM        *bool    `yaml:"allow_dm"`
	AllowGroups    *bool    `yaml:"allow_groups"`
	AllowedSenders []string `yaml:"allowed_senders"`
	PairedGroups   []string `yaml:"paired_groups"`
}

type TelegramConfig struct {
	BotToken      string `yaml:"bot_token"`
	WebhookSecret string `yaml:"webhook_secret"`
}

type DiscordConfig struct {
	BotToken  string `yaml:"bot_token"`
	PublicKey string `yaml:"public_key"` // Interaction署名検証用(HEX)
}

type SlackConfig struct {
	AppToken      string `yaml:"app_token"` // Socket Mode 用（将来利用）
	BotToken      string `yaml:"bot_token"` // chat.postMessage 用
	SigningSecret string `yaml:"signing_secret"`
}

// LogConfig はログ設定
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// DistributedConfig は分散実行設定
// YAML に distributed セクションがない場合、ゼロ値（Enabled=false）でv3互換動作
type DistributedConfig struct {
	Enabled         bool                       `yaml:"enabled"`
	Transports      map[string]TransportConfig `yaml:"transports"`
	CoderTimeoutSec int                        `yaml:"coder_timeout_sec"` // Coder SSH タイムアウト秒数（0以下は360とみなす）
	CoderRetryMax   int                        `yaml:"coder_retry_max"`   // Coder リトライ上限（0以下は2とみなす）
}

// TransportConfig はAgent別のTransport設定
type TransportConfig struct {
	Type             string `yaml:"type"`               // "local" or "ssh"
	RemoteHost       string `yaml:"remote_host"`        // SSH接続先（例: "192.168.1.100:22"）
	RemoteUser       string `yaml:"remote_user"`        // SSHユーザー名
	SSHKeyPath       string `yaml:"ssh_key_path"`       // SSH秘密鍵パス
	StrictHostKey    bool   `yaml:"strict_host_key"`    // true: known_hosts必須（本番用）、false: Insecureフォールバック許可
	RemoteAgentPath  string `yaml:"remote_agent_path"`  // リモートのrencrow-agentパス（例: "C:/Users/nyuki/rencrow-agent.exe"）
	RemoteConfigPath string `yaml:"remote_config_path"` // リモートのconfig.yamlパス（例: "C:/Users/nyuki/.rencrow/config.yaml"）
}

// IdleChatConfig はAgent間雑談モードの設定
type IdleChatConfig struct {
	Enabled                 bool                                  `yaml:"enabled"`                   // 雑談モードの有効化（デフォルト: false）
	Participants            []string                              `yaml:"participants"`              // 参加Agent名（デフォルト: ["mio", "shiro"]）
	IntervalMin             int                                   `yaml:"interval_min"`              // 雑談開始までのアイドル時間・分（デフォルト: 5）
	IntervalSec             int                                   `yaml:"interval_sec"`              // 雑談開始までのアイドル時間・秒（指定時は interval_min より優先）
	MaxTurns                int                                   `yaml:"max_turns"`                 // 1回の雑談の最大ターン数（デフォルト: 10）
	Temperature             float64                               `yaml:"temperature"`               // 雑談時の温度（デフォルト: 0.8）
	StoryDataDir            string                                `yaml:"story_data_dir"`            // 物語データJSONディレクトリ（デフォルト: "data/story"）
	ForecastExternalEnabled bool                                  `yaml:"forecast_external_enabled"` // true の場合のみ Forecast で外部 Coder API を明示利用する
	TopicGeneration         IdleChatTopicGenerationConfig         `yaml:"topic_generation"`          // お題候補生成・Judge設定
	DialogueInterestingness IdleChatDialogueInterestingnessConfig `yaml:"dialogue_interestingness"`  // 対話演出・品質判定設定
	SpeakerLLMOptions       map[string]IdleChatLLMOptions         `yaml:"speaker_llm_options"`       // 話者別LLMオプション
}

type IdleChatTopicGenerationConfig struct {
	Enabled                   bool                               `yaml:"enabled"`
	CandidatesPerAttempt      int                                `yaml:"candidates_per_attempt"`
	MaxAttempts               int                                `yaml:"max_attempts"`
	JudgeEnabled              bool                               `yaml:"judge_enabled"`
	MinJudgeTotal             int                                `yaml:"min_judge_total"`
	MinCategoryFit            int                                `yaml:"min_category_fit"`
	MinSafety                 int                                `yaml:"min_safety"`
	RecentTopicWindow         int                                `yaml:"recent_topic_window"`
	RecentSimilarityThreshold float64                            `yaml:"recent_similarity_threshold"`
	LogCandidates             bool                               `yaml:"log_candidates"`
	LogJudgeScores            bool                               `yaml:"log_judge_scores"`
	Prompts                   IdleChatTopicGenerationPromptPaths `yaml:"prompts"`
}

type IdleChatTopicGenerationPromptPaths struct {
	Common   string `yaml:"common"`
	Single   string `yaml:"single"`
	Double   string `yaml:"double"`
	External string `yaml:"external"`
	Movie    string `yaml:"movie"`
	News     string `yaml:"news"`
	Forecast string `yaml:"forecast"`
	Story    string `yaml:"story"`
	Judge    string `yaml:"judge"`
}

type IdleChatDialogueInterestingnessConfig struct {
	Enabled                   bool                                       `yaml:"enabled"`
	MaxTurnsPerTopic          int                                        `yaml:"max_turns_per_topic"`
	MinQualityScore           int                                        `yaml:"min_quality_score"`
	MaxQualityRetries         int                                        `yaml:"max_quality_retries"`
	EnforcePreviousUptake     bool                                       `yaml:"enforce_previous_uptake"`
	EnforceOneNewContribution bool                                       `yaml:"enforce_one_new_contribution"`
	EnforceCategoryAxis       bool                                       `yaml:"enforce_category_axis"`
	ForbidMetaLeak            bool                                       `yaml:"forbid_meta_leak"`
	ForbidUserQuestion        bool                                       `yaml:"forbid_user_question"`
	Utterance                 IdleChatDialogueUtteranceConfig            `yaml:"utterance"`
	Prompts                   IdleChatDialogueInterestingnessPromptPaths `yaml:"prompts"`
}

type IdleChatDialogueUtteranceConfig struct {
	MinRunes              int `yaml:"min_runes"`
	MaxRunes              int `yaml:"max_runes"`
	PreferredMaxSentences int `yaml:"preferred_max_sentences"`
}

type IdleChatDialogueInterestingnessPromptPaths struct {
	Common   string `yaml:"common"`
	Single   string `yaml:"single"`
	Double   string `yaml:"double"`
	External string `yaml:"external"`
	Movie    string `yaml:"movie"`
	News     string `yaml:"news"`
	Forecast string `yaml:"forecast"`
	Story    string `yaml:"story"`
}

type IdleChatLLMOptions struct {
	Think *bool `yaml:"think"` // true=Think, false=NoThink
}

// ConversationConfig は会話LLMの設定
type ConversationConfig struct {
	Enabled          bool   `yaml:"enabled"`           // 会話LLM機能の有効化（デフォルト: false）
	RedisURL         string `yaml:"redis_url"`         // Redis接続先（例: "redis://localhost:6379"）
	L1SQLitePath     string `yaml:"l1_sqlite_path"`    // L1 hot store SQLite path（任意）
	DuckDBPath       string `yaml:"duckdb_path"`       // DuckDBファイルパス（例: "/var/lib/rencrow/memory.duckdb"）
	VectorDBURL      string `yaml:"vectordb_url"`      // VectorDB gRPC接続先（例: "localhost:6334" for Qdrant）
	VectorCollection string `yaml:"vector_collection"` // 会話要約用Qdrant collection名。空の場合はrencrow_memory
	VectorDimension  int    `yaml:"vector_dimension"`  // 会話要約用embedding次元。0の場合は768
	EmbedProvider    string `yaml:"embed_provider"`    // Embedding provider（"ollama" または "openai"）。空の場合は従来の自動選択
	EmbedBaseURL     string `yaml:"embed_base_url"`    // Embedding専用Base URL。空の場合はprovider既定URLを使用
	EmbedModel       string `yaml:"embed_model"`       // Embedding用モデル（例: "nomic-embed-text"）。空の場合はembedding無効
	SummaryModel     string `yaml:"summary_model"`     // 要約用モデル（例: "Chat"）。空の場合はOllama chatモデルを使用
}

// HeartbeatConfig はハートビート（定期タスク）の設定
type HeartbeatConfig struct {
	Enabled  bool   `yaml:"enabled"`  // ハートビートの有効化（デフォルト: false）
	Interval int    `yaml:"interval"` // チェック間隔（分）、最小5分（デフォルト: 30）
	Channel  string `yaml:"channel"`  // 通知先チャネル（line, telegram, discord, slack）
	ChatID   string `yaml:"chat_id"`  // 通知先ID（LINE user ID / Telegram chat ID / Discord channel ID / Slack channel ID）
}

type GlossaryConfig struct {
	Enabled           bool     `yaml:"enabled"`
	DBPath            string   `yaml:"db_path"`
	RefreshIntervalHr int      `yaml:"refresh_interval_hr"`
	MaxEntries        int      `yaml:"max_entries"`
	FeedURLs          []string `yaml:"feed_urls"`
}

// SubagentConfig はサブエージェントシステムの設定
type SubagentConfig struct {
	Enabled       bool   `yaml:"enabled"`            // サブエージェント有効化（デフォルト: false）
	MaxIterations int    `yaml:"max_iterations"`     // ReActループ最大反復回数（デフォルト: 10）
	Provider      string `yaml:"provider,omitempty"` // LLMプロバイダー: "ollama"(default), "claude", "openai", "deepseek"
	Model         string `yaml:"model,omitempty"`    // 使用モデル（空=各プロバイダーのデフォルトモデルを使用）
}

// CapabilityConfig はケイパビリティ適応システムの設定（v4.1）
type CapabilityConfig struct {
	// ProbeLLMs: true の場合、起動時に各 LLM に疎通確認を実施する
	// false の場合は config に記載された情報だけでケイパビリティを決定する
	ProbeLLMs bool `yaml:"probe_llms"`

	// ToolRegistryDB: ToolRegistry の DuckDB ファイルパス（空の場合は ToolRegistry 無効）
	ToolRegistryDB string `yaml:"tool_registry_db"`

	// LLMQualityOverrides: モデル名 → 品質ランク（1〜5）の上書き設定
	LLMQualityOverrides map[string]int `yaml:"llm_quality_overrides"`
}

// SecurityConfig は実行ポリシーと監査設定
type SecurityConfig struct {
	Enabled           bool                `yaml:"enabled"`
	PolicyMode        string              `yaml:"policy_mode"`       // strict|balanced|dev
	NetworkScope      string              `yaml:"network_scope"`     // blocked|allowlist|full (optional: fallback to profile)
	NetworkAllowlist  []string            `yaml:"network_allowlist"` // host allowlist when network_scope=allowlist
	DenyCommands      []string            `yaml:"deny_commands"`
	WorkspaceEnforced bool                `yaml:"workspace_enforced"`
	Audit             SecurityAuditConfig `yaml:"audit"`
}

// SecurityAuditConfig は監査ログ出力設定
type SecurityAuditConfig struct {
	Enabled bool   `yaml:"enabled"`
	Backend string `yaml:"backend"` // jsonl|sqlite
	Path    string `yaml:"path"`
}

type SandboxConfig struct {
	Enabled                 bool                   `yaml:"enabled"`
	Storage                 string                 `yaml:"storage"`
	Root                    string                 `yaml:"root"`
	SQLitePath              string                 `yaml:"sqlite_path"`
	DenyOutsideSandboxWrite bool                   `yaml:"deny_outside_sandbox_write"`
	Promotion               SandboxPromotionConfig `yaml:"promotion"`
}

type SandboxPromotionConfig struct {
	RequireDiff                  bool   `yaml:"require_diff"`
	RequireReason                bool   `yaml:"require_reason"`
	RequireTestResult            bool   `yaml:"require_test_result"`
	RequireRollbackPlan          bool   `yaml:"require_rollback_plan"`
	RequireHumanApproval         bool   `yaml:"require_human_approval"`
	RequirePostApplyVerification bool   `yaml:"require_post_apply_verification"`
	ApplyRoot                    string `yaml:"apply_root"`
}

// ToolHarnessConfig は tool call 入力契約調停の runtime 設定。
// enabled / record_events は未指定時に true とみなし、現行 runtime 互換を保つ。
type ToolHarnessConfig struct {
	Enabled      *bool  `yaml:"enabled"`
	Mode         string `yaml:"mode"` // validate_then_repair|log_only|strict
	RecordEvents *bool  `yaml:"record_events"`
	LogPath      string `yaml:"log_path"`
}

func (c ToolHarnessConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c ToolHarnessConfig) ShouldRecordEvents() bool {
	return c.RecordEvents == nil || *c.RecordEvents
}

// DCISessionLogSource はDCIが参照するセッションログソースの設定
type DCISessionLogSource struct {
	Name    string `yaml:"name"`     // 表示名 ("rencrow", "codex", "claude")
	PathDir string `yaml:"path_dir"` // ベースディレクトリ（$HOME等の環境変数展開あり）
	Format  string `yaml:"format"`   // "rencrow" | "codex" | "claude"
}

type DCIConfig struct {
	Enabled             *bool                 `yaml:"enabled"`
	Storage             string                `yaml:"storage"`
	TracePath           string                `yaml:"trace_path"`
	SQLitePath          string                `yaml:"sqlite_path"`
	CorpusAllowlist     []string              `yaml:"corpus_allowlist"`
	CorpusDenylist      []string              `yaml:"corpus_denylist"`
	KnowledgeFTSDomains []string              `yaml:"knowledge_fts_domains"`
	ExplicitKeywords    []string              `yaml:"explicit_keywords"`
	SessionLogSources   []DCISessionLogSource `yaml:"session_log_sources"`
	MaxSeconds          int                   `yaml:"max_seconds"`
	MaxSteps            int                   `yaml:"max_steps"`
	MaxCandidateFiles   int                   `yaml:"max_candidate_files"`
	MaxFilesRead        int                   `yaml:"max_files_read"`
	MaxEvidence         int                   `yaml:"max_evidence"`
	MaxSnippetChars     int                   `yaml:"max_snippet_chars"`
}

func (c DCIConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type SkillGovernanceConfig struct {
	Enabled            *bool                       `yaml:"enabled"`
	Storage            string                      `yaml:"storage"`
	RegistryPath       string                      `yaml:"registry_path"`
	SQLitePath         string                      `yaml:"sqlite_path"`
	SkillRoots         []string                    `yaml:"skill_roots"`
	RequiredForCoder   bool                        `yaml:"required_for_coder"`
	RequiredForWorker  bool                        `yaml:"required_for_worker"`
	WarnIfSkillNotUsed bool                        `yaml:"warn_if_skill_not_used"`
	ContributionGate   SkillContributionGateConfig `yaml:"contribution_gate"`
}

type SkillContributionGateConfig struct {
	Enabled                   bool `yaml:"enabled"`
	RequireOpenClosedPRSearch bool `yaml:"require_open_closed_pr_search"`
	RequireRealProblem        bool `yaml:"require_real_problem"`
	RequireCompleteDiffReview bool `yaml:"require_complete_diff_review"`
	RequireHumanApproval      bool `yaml:"require_human_approval"`
	OneProblemPerPR           bool `yaml:"one_problem_per_pr"`
}

func (c SkillGovernanceConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type WorkstreamConfig struct {
	Enabled                  *bool  `yaml:"enabled"`
	Storage                  string `yaml:"storage"`
	LogPath                  string `yaml:"log_path"`
	SQLitePath               string `yaml:"sqlite_path"`
	VaultRoot                string `yaml:"vault_root"`
	RequireSuccessCriteria   bool   `yaml:"require_success_criteria"`
	RequireVerification      bool   `yaml:"require_verification"`
	DraftReportOnlyHeartbeat bool   `yaml:"draft_report_only_heartbeat"`
}

func (c WorkstreamConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type RevenueConfig struct {
	Enabled                         *bool  `yaml:"enabled"`
	Storage                         string `yaml:"storage"`
	LogPath                         string `yaml:"log_path"`
	SQLitePath                      string `yaml:"sqlite_path"`
	ProhibitSuccessGuarantee        bool   `yaml:"prohibit_success_guarantee"`
	RequireCustomerVoicePermission  bool   `yaml:"require_customer_voice_permission"`
	ExternalPublishRequiresApproval bool   `yaml:"external_publish_requires_approval"`
	HighTicketOfferRequiresApproval bool   `yaml:"high_ticket_offer_requires_approval"`
}

func (c RevenueConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type PersonaArchitectureConfig struct {
	Enabled                        *bool  `yaml:"enabled"`
	Storage                        string `yaml:"storage"`
	LogPath                        string `yaml:"log_path"`
	SQLitePath                     string `yaml:"sqlite_path"`
	CharacterRoot                  string `yaml:"character_root"`
	TriggerCategoryPath            string `yaml:"trigger_category_path"`
	CanonicalResponsePath          string `yaml:"canonical_response_path"`
	CanonicalResponseCooldownTurns int    `yaml:"canonical_response_cooldown_turns"`
	CanonicalResponseMaxPerSession int    `yaml:"canonical_response_max_per_session"`
	RequireLorePersonaSplit        bool   `yaml:"require_lore_persona_split"`
	RequireTriggerCategories       bool   `yaml:"require_trigger_categories"`
	HumanReviewRequiredForMeta     bool   `yaml:"human_review_required_for_meta"`
	RequireSessionKeying           bool   `yaml:"require_session_keying"`
	MaxTriggerCandidates           int    `yaml:"max_trigger_candidates"`
}

func (c PersonaArchitectureConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type BrowserTraceToAPIConfig struct {
	Enabled                     *bool    `yaml:"enabled"`
	Storage                     string   `yaml:"storage"`
	LogPath                     string   `yaml:"log_path"`
	SQLitePath                  string   `yaml:"sqlite_path"`
	ReadOnlyOnly                bool     `yaml:"read_only_only"`
	RequireTermsReview          bool     `yaml:"require_terms_review"`
	RequireHumanApprovalPromote bool     `yaml:"require_human_approval_for_promote"`
	GenerateOpenAPI             bool     `yaml:"generate_openapi"`
	GenerateCoverageReport      bool     `yaml:"generate_coverage_report"`
	AcceptedPaths               []string `yaml:"accepted_paths"`
	DenyMethods                 []string `yaml:"deny_methods"`
	DenySensitiveFlows          []string `yaml:"deny_sensitive_flows"`
}

func (c BrowserTraceToAPIConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type ComplexityHotspotConfig struct {
	Enabled                      *bool    `yaml:"enabled"`
	Storage                      string   `yaml:"storage"`
	LogPath                      string   `yaml:"log_path"`
	SQLitePath                   string   `yaml:"sqlite_path"`
	DefaultMode                  string   `yaml:"default_mode"`
	MaxHotspots                  int      `yaml:"max_hotspots"`
	ExcludeDirs                  []string `yaml:"exclude_dirs"`
	AutoApply                    bool     `yaml:"auto_apply"`
	RequireHumanApprovalForPatch bool     `yaml:"require_human_approval_for_patch"`
	OneHotspotPerPR              bool     `yaml:"one_hotspot_per_pr"`
}

func (c ComplexityHotspotConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type SuperAgentHarnessConfig struct {
	Enabled                      *bool  `yaml:"enabled"`
	Storage                      string `yaml:"storage"`
	LogPath                      string `yaml:"log_path"`
	SQLitePath                   string `yaml:"sqlite_path"`
	MaxParallelSubagents         int    `yaml:"max_parallel_subagents"`
	MaxContextPackTokens         int    `yaml:"max_context_pack_tokens"`
	RunQueueSchedulerEnabled     bool   `yaml:"run_queue_scheduler_enabled"`
	RunQueueSchedulerIntervalSec int    `yaml:"run_queue_scheduler_interval_sec"`
	RunQueueSchedulerClaimLimit  int    `yaml:"run_queue_scheduler_claim_limit"`
	RequireScope                 bool   `yaml:"require_scope"`
	RequireTerminationCondition  bool   `yaml:"require_termination_condition"`
	ReturnSummaryOnly            bool   `yaml:"return_summary_only"`
	PromotionGateRequired        bool   `yaml:"promotion_gate_required"`
	ExternalSendRequiresApproval bool   `yaml:"external_send_requires_approval"`
	TraceAgentRun                bool   `yaml:"trace_agent_run"`
}

func (c SuperAgentHarnessConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type AIWorkflowConfig struct {
	Enabled                         *bool    `yaml:"enabled"`
	Storage                         string   `yaml:"storage"`
	LogPath                         string   `yaml:"log_path"`
	SQLitePath                      string   `yaml:"sqlite_path"`
	ProjectMemoryRoot               string   `yaml:"project_memory_root"`
	WorktreeBaseDir                 string   `yaml:"worktree_base_dir"`
	RequiredBeforeModify            bool     `yaml:"required_before_modify"`
	WorktreeRequiredForWrite        bool     `yaml:"worktree_required_for_write"`
	RequiredCLITools                []string `yaml:"required_cli_tools"`
	ContextTrackingEnabled          bool     `yaml:"context_tracking_enabled"`
	ContextBudgetTokens             int      `yaml:"context_budget_tokens"`
	ContextBudgetWarnRatio          float64  `yaml:"context_budget_warn_ratio"`
	ContextBudgetStopRatio          float64  `yaml:"context_budget_stop_ratio"`
	HeavyWorkerEnabled              bool     `yaml:"heavy_worker_enabled"`
	HeavyWorkerRequireReason        bool     `yaml:"heavy_worker_require_reason"`
	HeavyWorkerFileThreshold        int      `yaml:"heavy_worker_file_threshold"`
	HeavyWorkerSpecThreshold        int      `yaml:"heavy_worker_spec_threshold"`
	HeavyWorkerRetryThreshold       int      `yaml:"heavy_worker_retry_threshold"`
	ExternalControlAllowedActors    []string `yaml:"external_control_allowed_actors"`
	ExternalControlAllowedChannels  []string `yaml:"external_control_allowed_channels"`
	ExternalControlAllowedActions   []string `yaml:"external_control_allowed_actions"`
	ExternalControlApprovalRequired []string `yaml:"external_control_approval_required"`
}

func (c AIWorkflowConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type KnowledgeMemoryConfig struct {
	Enabled                     *bool  `yaml:"enabled"`
	Storage                     string `yaml:"storage"`
	LogPath                     string `yaml:"log_path"`
	SQLitePath                  string `yaml:"sqlite_path"`
	ProtectPersonalArchive      bool   `yaml:"protect_personal_archive"`
	DreamRequiresHumanReview    bool   `yaml:"dream_requires_human_review"`
	DailyIntakePromoteToStaging bool   `yaml:"daily_intake_promote_to_staging"`
}

func (c KnowledgeMemoryConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}
