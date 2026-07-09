package rencrowclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rencrow API %s %s failed: status=%d body=%s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

type Option func(*Client)

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func New(baseURL string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("invalid base URL: %w", err)
	}
	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

type SuperAgentStatus struct {
	AgentRuns       []AgentRun              `json:"agent_runs"`
	SubagentTasks   []SubagentTask          `json:"subagent_tasks"`
	ContextPacks    []ContextPack           `json:"context_packs"`
	MessageChannels []MessageChannel        `json:"message_channels"`
	TraceEvents     []TraceEvent            `json:"trace_events"`
	RunQueue        []RunQueueItem          `json:"run_queue"`
	RuntimeConfig   SuperAgentRuntimeConfig `json:"runtime_config"`
}

type SuperAgentRuntimeConfig struct {
	RunQueueSchedulerEnabled     bool `json:"run_queue_scheduler_enabled"`
	RunQueueSchedulerIntervalSec int  `json:"run_queue_scheduler_interval_sec"`
	RunQueueSchedulerClaimLimit  int  `json:"run_queue_scheduler_claim_limit"`
}

type AgentRun struct {
	RunID        string    `json:"run_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	ParentRunID  string    `json:"parent_run_id,omitempty"`
	AgentType    string    `json:"agent_type"`
	Goal         string    `json:"goal,omitempty"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	Summary      string    `json:"summary,omitempty"`
}

type SubagentTask struct {
	SubagentID           string    `json:"subagent_id"`
	ParentRunID          string    `json:"parent_run_id"`
	AgentType            string    `json:"agent_type"`
	Task                 string    `json:"task"`
	Scope                []string  `json:"scope"`
	Tools                []string  `json:"tools,omitempty"`
	TerminationCondition string    `json:"termination_condition"`
	OutputPath           string    `json:"output_path,omitempty"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
	CompletedAt          time.Time `json:"completed_at,omitempty"`
}

type ContextPack struct {
	ContextPackID   string    `json:"context_pack_id"`
	RunID           string    `json:"run_id"`
	WorkstreamID    string    `json:"workstream_id,omitempty"`
	Summary         string    `json:"summary"`
	IncludedSources []string  `json:"included_sources,omitempty"`
	TokenEstimate   int       `json:"token_estimate,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type MessageChannel struct {
	ChannelID      string    `json:"channel_id"`
	ChannelType    string    `json:"channel_type"`
	Name           string    `json:"name,omitempty"`
	AuthScope      string    `json:"auth_scope,omitempty"`
	AllowedActions []string  `json:"allowed_actions,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type TraceEvent struct {
	EventID        string    `json:"event_id"`
	ParentEventID  string    `json:"parent_event_id,omitempty"`
	RunID          string    `json:"run_id,omitempty"`
	EventType      string    `json:"event_type"`
	Actor          string    `json:"actor,omitempty"`
	PayloadSummary string    `json:"payload_summary,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
}

type RunQueueItem struct {
	QueueID      string    `json:"queue_id"`
	RunID        string    `json:"run_id,omitempty"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	Goal         string    `json:"goal"`
	Action       string    `json:"action"`
	Status       string    `json:"status"`
	Priority     int       `json:"priority,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	NotBefore    time.Time `json:"not_before,omitempty"`
	ClaimedAt    time.Time `json:"claimed_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type RunQueueClaimResponse struct {
	Claimed bool         `json:"claimed"`
	Item    RunQueueItem `json:"item,omitempty"`
}

type RunQueueCompleteRequest struct {
	QueueID string `json:"queue_id"`
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type RunQueueCompleteResponse struct {
	Completed bool         `json:"completed"`
	Item      RunQueueItem `json:"item,omitempty"`
}

type RuntimeConfig struct {
	STTStreamURL     string                     `json:"stt_stream_url,omitempty"`
	STTBaseURL       string                     `json:"stt_base_url,omitempty"`
	TTSBaseURL       string                     `json:"tts_base_url,omitempty"`
	TTSHealthPath    string                     `json:"tts_health_path,omitempty"`
	LLMOpsConfigured bool                       `json:"llm_ops_configured"`
	LLMOpsEnabled    bool                       `json:"llm_ops_enabled"`
	LLMOpsBaseURL    string                     `json:"llm_ops_base_url,omitempty"`
	LocalLLM         LocalLLMRuntimeConfig      `json:"local_llm,omitempty"`
	RuntimeReadiness RuntimeDependencyReadiness `json:"runtime_readiness,omitempty"`
}

type LocalLLMRuntimeConfig struct {
	Enabled           bool   `json:"enabled"`
	Provider          string `json:"provider,omitempty"`
	ChatBaseURL       string `json:"chat_base_url,omitempty"`
	WorkerBaseURL     string `json:"worker_base_url,omitempty"`
	ChatWorkerBaseURL string `json:"chat_worker_base_url,omitempty"`
	HeavyBaseURL      string `json:"heavy_base_url,omitempty"`
	WildBaseURL       string `json:"wild_base_url,omitempty"`
	ChatModel         string `json:"chat_model,omitempty"`
	WorkerModel       string `json:"worker_model,omitempty"`
	HeavyModel        string `json:"heavy_model,omitempty"`
	WildModel         string `json:"wild_model,omitempty"`
	TimeoutSec        int    `json:"timeout_sec,omitempty"`
	GlobalConcurrency int    `json:"global_concurrency,omitempty"`
	ModelConcurrency  int    `json:"model_concurrency,omitempty"`
}

type RuntimeDependencyReadiness struct {
	SlackCredentialsPresent      *bool `json:"slack_credentials_present"`
	SlackWebhookRegistered       *bool `json:"slack_webhook_registered"`
	SlackFilePayloadPipeline     *bool `json:"slack_file_payload_pipeline"`
	DiscordCredentialsPresent    *bool `json:"discord_credentials_present"`
	DiscordWebhookRegistered     *bool `json:"discord_webhook_registered"`
	DiscordFilePayloadPipeline   *bool `json:"discord_file_payload_pipeline"`
	TelegramCredentialsPresent   *bool `json:"telegram_credentials_present"`
	TelegramWebhookRegistered    *bool `json:"telegram_webhook_registered"`
	TelegramFilePayloadPipeline  *bool `json:"telegram_file_payload_pipeline"`
	STTGatewayEnvPresent         *bool `json:"stt_gateway_env_present"`
	STTGatewayConfigPresent      *bool `json:"stt_gateway_config_present"`
	TTSProviderEnvPresent        *bool `json:"tts_provider_env_present"`
	TTSProviderConfigPresent     *bool `json:"tts_provider_config_present"`
	DistributedEnabled           *bool `json:"distributed_enabled"`
	DistributedTransportsPresent *bool `json:"distributed_transports_present"`
	DistributedSSHConfigured     *bool `json:"distributed_ssh_configured"`
	DistributedSSHConnected      *bool `json:"distributed_ssh_connected"`
	DistributedLocalTransport    *bool `json:"distributed_local_transport"`
	ConversationEnabled          *bool `json:"conversation_enabled"`
	L1SQLiteConfigPresent        *bool `json:"l1_sqlite_config_present"`
	MemoryLayersAvailable        *bool `json:"memory_layers_available"`
	MemoryLayersStatus           *bool `json:"memory_layers_status_available"`
	SourceRegistryAvailable      *bool `json:"source_registry_available"`
	SourceRegistryStatus         *bool `json:"source_registry_status_available"`
	DomainGraphAvailable         *bool `json:"domain_graph_available"`
	DomainGraphStatus            *bool `json:"domain_graph_status_available"`
	KnowledgeMemoryEnabled       *bool `json:"knowledge_memory_enabled"`
	KnowledgeMemoryStatus        *bool `json:"knowledge_memory_status_available"`
	BrowserTraceAPIEnabled       *bool `json:"browser_trace_api_enabled"`
	BrowserTraceAPIStatus        *bool `json:"browser_trace_api_status_available"`
	BrowserTraceAPIFetcher       *bool `json:"browser_trace_api_fetcher_available"`
	SandboxEnabled               *bool `json:"sandbox_enabled"`
	SandboxStatusAvailable       *bool `json:"sandbox_status_available"`
}

type RuntimeHealthReport struct {
	Status    string               `json:"status"`
	Checks    []RuntimeHealthCheck `json:"checks"`
	Timestamp string               `json:"timestamp"`
}

type RuntimeHealthCheck struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Message    string  `json:"message,omitempty"`
	DurationMS float64 `json:"duration_ms"`
}

type LLMOpsStatus struct {
	Roles  map[string]LLMOpsRoleState `json:"roles"`
	Memory LLMOpsMemoryStatus         `json:"memory,omitempty"`
}

type LLMOpsHealth struct {
	Status string `json:"status"`
	Daemon string `json:"daemon,omitempty"`
}

type llmOpsStopRequest struct {
	Roles []string `json:"roles"`
}

type llmOpsSelectionRequest struct {
	Selection string `json:"selection"`
}

type LLMOpsRoleState struct {
	HealthOK *bool `json:"health_ok,omitempty"`
	Halted   *bool `json:"halted,omitempty"`
}

type LLMOpsMemoryStatus struct {
	LLMByRole map[string]LLMOpsMemoryRole `json:"llm_by_role,omitempty"`
}

type LLMOpsMemoryRole struct {
	Role   string  `json:"role,omitempty"`
	Model  string  `json:"model,omitempty"`
	Port   int     `json:"port,omitempty"`
	PID    *int    `json:"pid,omitempty"`
	RSSMiB float64 `json:"rss_mib,omitempty"`
}

type DebugSystemSnapshot struct {
	UpdatedAt string             `json:"updated_at"`
	Audio     DebugAudioSnapshot `json:"audio"`
}

type DebugAudioSnapshot struct {
	STTBaseURL string `json:"stt_base_url,omitempty"`
	TTSBaseURL string `json:"tts_base_url,omitempty"`
	STTOK      bool   `json:"stt_ok"`
	TTSLiveOK  bool   `json:"tts_live_ok"`
	TTSReadyOK bool   `json:"tts_ready_ok"`
	STTHealth  string `json:"stt_health,omitempty"`
	TTSLive    string `json:"tts_live,omitempty"`
	TTSReady   string `json:"tts_ready,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type CommandRunRequest struct {
	CommandName  string `json:"command_name"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	Agent        string `json:"agent,omitempty"`
	Text         string `json:"text,omitempty"`
	Input        string `json:"input,omitempty"`
}

type CommandRunResponse struct {
	Command      CommandRegistry `json:"command"`
	Event        WorkflowEvent   `json:"event"`
	EventID      string          `json:"event_id,omitempty"`
	SkillEventID string          `json:"skill_event_id,omitempty"`
	CommandName  string          `json:"command_name,omitempty"`
	Status       string          `json:"status,omitempty"`
}

type AIWorkflowStatus struct {
	WorkflowEvents       []WorkflowEvent      `json:"workflow_events"`
	ProjectMemoryIndexes []ProjectMemoryIndex `json:"project_memory_indexes"`
	WorktreeRegistries   []WorktreeRegistry   `json:"worktree_registries"`
	CommandRegistries    []CommandRegistry    `json:"command_registries"`
	ContextUsages        []ContextUsage       `json:"context_usages"`
	ContextBudgetPolicy  ContextBudgetPolicy  `json:"context_budget_policy"`
}

type WorkflowEvent struct {
	EventID       string    `json:"event_id"`
	ParentEventID string    `json:"parent_event_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	EventType     string    `json:"event_type"`
	Agent         string    `json:"agent,omitempty"`
	Repo          string    `json:"repo,omitempty"`
	WorktreeID    string    `json:"worktree_id,omitempty"`
	CommandName   string    `json:"command_name,omitempty"`
	SkillName     string    `json:"skill_name,omitempty"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
	Summary       string    `json:"summary,omitempty"`
}

type ProjectMemoryIndex struct {
	ID          string    `json:"id"`
	Repo        string    `json:"repo"`
	FilePath    string    `json:"file_path"`
	MemoryType  string    `json:"memory_type"`
	Title       string    `json:"title,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type WorktreeRegistry struct {
	WorktreeID string    `json:"worktree_id"`
	Repo       string    `json:"repo"`
	Path       string    `json:"path"`
	Branch     string    `json:"branch"`
	Purpose    string    `json:"purpose,omitempty"`
	OwnerAgent string    `json:"owner_agent,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	ClosedAt   time.Time `json:"closed_at,omitempty"`
}

type CommandRegistry struct {
	CommandName   string    `json:"command_name"`
	FilePath      string    `json:"file_path"`
	Description   string    `json:"description,omitempty"`
	DefaultAgent  string    `json:"default_agent,omitempty"`
	RequiredSkill string    `json:"required_skill,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ContextUsage struct {
	EventID         string    `json:"event_id"`
	SessionID       string    `json:"session_id,omitempty"`
	Agent           string    `json:"agent"`
	Model           string    `json:"model,omitempty"`
	InputTokens     int       `json:"input_tokens,omitempty"`
	OutputTokens    int       `json:"output_tokens,omitempty"`
	ContextTokens   int       `json:"context_tokens,omitempty"`
	ToolCallCount   int       `json:"tool_call_count,omitempty"`
	DCICallCount    int       `json:"dci_call_count,omitempty"`
	RepairCount     int       `json:"repair_count,omitempty"`
	LatencyMS       int       `json:"latency_ms,omitempty"`
	EstimatedCost   float64   `json:"estimated_cost,omitempty"`
	KVCacheEstimate float64   `json:"kv_cache_estimate,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type ContextBudgetPolicy struct {
	MaxContextTokens int     `json:"max_context_tokens"`
	WarnAtRatio      float64 `json:"warn_at_ratio"`
	StopAtRatio      float64 `json:"stop_at_ratio"`
}

type ToolHarnessStatus struct {
	Items []ToolHarnessEvent `json:"items"`
}

type ToolHarnessEvent struct {
	EventID          string               `json:"event_id"`
	ToolName         string               `json:"tool_name"`
	RawInputHash     string               `json:"raw_input_hash"`
	ValidationStatus string               `json:"validation_status"`
	Repairs          []ToolHarnessRepair  `json:"repairs_applied,omitempty"`
	RelationDefaults []ToolHarnessDefault `json:"relation_defaults_applied,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
}

type ToolHarnessRepair struct {
	Type       string   `json:"type"`
	Path       []string `json:"path,omitempty"`
	BeforeType string   `json:"before_type,omitempty"`
	AfterType  string   `json:"after_type,omitempty"`
	Note       string   `json:"note,omitempty"`
}

type ToolHarnessDefault struct {
	Field  string `json:"field"`
	Value  any    `json:"value,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type DCIRecentStatus struct {
	Items []DCISearchTrace `json:"items"`
}

type DCISearchRequest struct {
	Query string `json:"query"`
}

type DCISearchResult struct {
	Pack  DCIEvidencePack `json:"pack"`
	Trace DCISearchTrace  `json:"trace"`
}

type DCIEvidencePack struct {
	EventID      string        `json:"event_id"`
	Query        string        `json:"query"`
	Intent       string        `json:"intent,omitempty"`
	CorpusScope  []string      `json:"corpus_scope"`
	Evidence     []DCIEvidence `json:"evidence"`
	DerivedTerms []string      `json:"derived_terms,omitempty"`
	Confidence   float64       `json:"confidence"`
	Limitations  []string      `json:"limitations,omitempty"`
}

type DCIEvidence struct {
	EvidenceID string  `json:"evidence_id"`
	SourceID   string  `json:"source_id,omitempty"`
	FilePath   string  `json:"file_path"`
	Heading    string  `json:"heading,omitempty"`
	LineStart  int     `json:"line_start"`
	LineEnd    int     `json:"line_end"`
	Snippet    string  `json:"snippet"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence"`
}

type DCISearchTrace struct {
	EventID            string          `json:"event_id"`
	StartedAt          time.Time       `json:"started_at"`
	EndedAt            time.Time       `json:"ended_at"`
	Actor              string          `json:"actor"`
	Mode               string          `json:"mode"`
	UserQuery          string          `json:"user_query"`
	CorpusScope        []string        `json:"corpus_scope"`
	Steps              []DCISearchStep `json:"steps"`
	FinalEvidenceCount int             `json:"final_evidence_count"`
	Status             string          `json:"status"`
	ErrorMessage       string          `json:"error_message,omitempty"`
}

type DCISearchStep struct {
	StepNo       int       `json:"step_no"`
	Tool         string    `json:"tool"`
	CommandText  string    `json:"command_text,omitempty"`
	FilePath     string    `json:"file_path,omitempty"`
	ResultCount  int       `json:"result_count,omitempty"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type KnowledgeMemoryStatus struct {
	PersonalArchive   []KnowledgePersonalArchiveEntry `json:"personal_archive"`
	CreativeKnowledge []KnowledgeCreativeItem         `json:"creative_knowledge"`
	NewsKnowledge     []KnowledgeNewsItem             `json:"news_knowledge"`
	DailyIntakeRules  []KnowledgeDailyIntakeRule      `json:"daily_intake_rules"`
	TemporalMarkers   []KnowledgeTemporalMarker       `json:"temporal_markers"`
	DreamRuns         []KnowledgeDreamRun             `json:"dream_runs"`
}

type KnowledgePersonalArchiveEntry struct {
	EntryID      string    `json:"entry_id"`
	UserID       string    `json:"user_id"`
	SourceRef    string    `json:"source_ref,omitempty"`
	OriginalText string    `json:"original_text"`
	Protected    bool      `json:"protected"`
	CreatedAt    time.Time `json:"created_at"`
}

type KnowledgeCreativeItem struct {
	ItemID       string    `json:"item_id"`
	Title        string    `json:"title"`
	CreatorNames []string  `json:"creator_names,omitempty"`
	WorkType     string    `json:"work_type,omitempty"`
	RelatedWorks []string  `json:"related_works,omitempty"`
	ContentHints []string  `json:"content_hints,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type KnowledgeNewsItem struct {
	ItemID    string    `json:"item_id"`
	Source    string    `json:"source"`
	Topic     string    `json:"topic"`
	EventDate string    `json:"event_date,omitempty"`
	URL       string    `json:"url,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Durable   bool      `json:"durable"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type KnowledgeDailyIntakeRule struct {
	RuleID     string    `json:"rule_id"`
	UserID     string    `json:"user_id"`
	Topic      string    `json:"topic"`
	SourceHint string    `json:"source_hint,omitempty"`
	Cadence    string    `json:"cadence"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type KnowledgeTemporalMarker struct {
	MarkerID    string    `json:"marker_id"`
	UserID      string    `json:"user_id,omitempty"`
	Layer       string    `json:"layer"`
	ReferenceID string    `json:"reference_id"`
	Summary     string    `json:"summary"`
	AccessCount int       `json:"access_count,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type KnowledgeDreamRun struct {
	RunID        string    `json:"run_id"`
	Scope        []string  `json:"scope,omitempty"`
	IdeaSeeds    []string  `json:"idea_seeds,omitempty"`
	Status       string    `json:"status"`
	ReviewStatus string    `json:"review_status"`
	CreatedAt    time.Time `json:"created_at"`
}

type KnowledgeMemoryCreateResponse struct {
	Status string `json:"status"`
}

type KnowledgeMemoryReviewRequest struct {
	DetailType   string `json:"detail_type"`
	ID           string `json:"id"`
	ReviewStatus string `json:"review_status"`
	Promote      bool   `json:"promote,omitempty"`
	ReviewedBy   string `json:"reviewed_by,omitempty"`
}

type KnowledgeMemoryReviewResponse struct {
	Status       string                          `json:"status"`
	DetailType   string                          `json:"detail_type"`
	ID           string                          `json:"id"`
	ReviewStatus string                          `json:"review_status"`
	Promoted     bool                            `json:"promoted"`
	AutoPromote  bool                            `json:"auto_promote"`
	ReviewedBy   string                          `json:"reviewed_by,omitempty"`
	Comparison   KnowledgeMemoryReviewComparison `json:"comparison"`
}

type KnowledgeMemoryReviewComparison struct {
	CurrentStatus string          `json:"current_status"`
	TargetStatus  string          `json:"target_status"`
	CurrentItem   json.RawMessage `json:"current_item,omitempty"`
	TargetItem    json.RawMessage `json:"target_item,omitempty"`
	FormalTarget  string          `json:"formal_target"`
}

type SourceRegistryStatus struct {
	Entries []SourceRegistryEntry `json:"entries"`
}

type SourceRegistryEntry struct {
	SourceID         string         `json:"source_id"`
	URL              string         `json:"url"`
	Kind             string         `json:"kind"`
	TrustScore       float64        `json:"trust_score"`
	FetchIntervalSec int64          `json:"fetch_interval_sec"`
	LicenseNote      string         `json:"license_note"`
	Enabled          bool           `json:"enabled"`
	Meta             map[string]any `json:"meta,omitempty"`
	LastFetchedAt    string         `json:"last_fetched_at,omitempty"`
	LastStatus       string         `json:"last_status,omitempty"`
	LastError        string         `json:"last_error,omitempty"`
	CreatedAt        string         `json:"created_at,omitempty"`
	UpdatedAt        string         `json:"updated_at,omitempty"`
}

type SourceRegistryStagingStatus struct {
	Items []SourceRegistryStagingItem `json:"items"`
}

type SourceRegistryStagingItem struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Namespace        string         `json:"namespace"`
	EventID          string         `json:"event_id"`
	SourceID         string         `json:"source_id"`
	SourceURL        string         `json:"source_url"`
	FetchedAt        string         `json:"fetched_at,omitempty"`
	PublishedAt      string         `json:"published_at,omitempty"`
	RawText          string         `json:"raw_text"`
	SummaryDraft     string         `json:"summary_draft"`
	Keywords         []string       `json:"keywords"`
	LicenseNote      string         `json:"license_note"`
	ValidationStatus string         `json:"validation_status"`
	Meta             map[string]any `json:"meta,omitempty"`
	CreatedAt        string         `json:"created_at,omitempty"`
	UpdatedAt        string         `json:"updated_at,omitempty"`
}

type SourceRegistryValidateRequest struct {
	ID                         string   `json:"id"`
	MinimumTrustScore          *float64 `json:"minimum_trust_score,omitempty"`
	AutoPromoteMemoryCandidate bool     `json:"auto_promote_memory_candidate,omitempty"`
}

type SourceRegistryValidationResponse struct {
	Result SourceRegistryValidationResult `json:"result"`
}

type SourceRegistryValidationResult struct {
	ItemID            string                          `json:"ItemID"`
	Passed            bool                            `json:"Passed"`
	Status            string                          `json:"Status"`
	Issues            []SourceRegistryValidationIssue `json:"Issues"`
	PromotedMemoryID  string                          `json:"PromotedMemoryID,omitempty"`
	PromotedNamespace string                          `json:"PromotedNamespace,omitempty"`
}

type SourceRegistryValidationIssue struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type SourceRegistryPromoteRequest struct {
	ID              string   `json:"id"`
	Target          string   `json:"target"`
	Category        string   `json:"category,omitempty"`
	Domain          string   `json:"domain,omitempty"`
	EntityType      string   `json:"entity_type,omitempty"`
	EntityID        string   `json:"entity_id,omitempty"`
	RelationType    string   `json:"relation_type,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	TargetNamespace string   `json:"target_namespace,omitempty"`
	PromotedBy      string   `json:"promoted_by,omitempty"`
}

type SourceRegistryPromotionResponse struct {
	Target string         `json:"target"`
	Item   map[string]any `json:"item"`
}

type DomainGraphAssertionsRequest struct {
	Domain           string
	EntityType       string
	EntityID         string
	RelationType     string
	SourceID         string
	ValidationStatus string
	Limit            int
	Offset           int
}

type DomainGraphAssertion struct {
	ID               string         `json:"id"`
	StagingID        string         `json:"staging_id"`
	Domain           string         `json:"domain"`
	EntityType       string         `json:"entity_type"`
	EntityID         string         `json:"entity_id,omitempty"`
	RelationType     string         `json:"relation_type,omitempty"`
	SourceID         string         `json:"source_id"`
	SourceURL        string         `json:"source_url,omitempty"`
	RawHash          string         `json:"raw_hash"`
	Summary          string         `json:"summary"`
	Confidence       float64        `json:"confidence"`
	ValidationStatus string         `json:"validation_status"`
	Evidence         map[string]any `json:"evidence"`
	CreatedAt        string         `json:"created_at"`
	UpdatedAt        string         `json:"updated_at"`
}

type DomainGraphAssertionsResponse struct {
	Items  []DomainGraphAssertion `json:"items"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
	Total  int                    `json:"total"`
}

type MemoryLayersRequest struct {
	SessionID string
	Namespace string
	Domain    string
	Limit     int
}

type MemoryLayersStatus struct {
	SessionID string                      `json:"session_id"`
	Namespace string                      `json:"namespace"`
	Domain    string                      `json:"domain"`
	L0        []MemoryLayerEvent          `json:"l0"`
	L1        []MemoryLayerEvent          `json:"l1"`
	L2        []MemoryLayerThreadSummary  `json:"l2"`
	L3        []MemoryLayerEvent          `json:"l3"`
	L3Qdrant  []MemoryLayerQdrantDocument `json:"l3_qdrant"`
}

type MemoryLayerEvent struct {
	ID          string         `json:"ID"`
	Namespace   string         `json:"Namespace,omitempty"`
	SessionID   string         `json:"SessionID,omitempty"`
	ThreadID    int64          `json:"ThreadID,omitempty"`
	Speaker     string         `json:"Speaker,omitempty"`
	Message     string         `json:"Message"`
	Meta        map[string]any `json:"Meta,omitempty"`
	MemoryState string         `json:"MemoryState,omitempty"`
	Layer       string         `json:"Layer"`
	Source      string         `json:"Source,omitempty"`
	CreatedAt   string         `json:"CreatedAt"`
	UpdatedAt   string         `json:"UpdatedAt,omitempty"`
}

type MemoryLayerThreadSummary struct {
	ThreadID  int64    `json:"thread_id"`
	SessionID string   `json:"session_id,omitempty"`
	Domain    string   `json:"domain,omitempty"`
	Summary   string   `json:"summary"`
	Keywords  []string `json:"keywords,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	StartTime string   `json:"ts_start,omitempty"`
	EndTime   string   `json:"ts_end,omitempty"`
	IsNovel   bool     `json:"is_novel,omitempty"`
	Score     float64  `json:"score,omitempty"`
}

type MemoryLayerQdrantDocument struct {
	ID        string         `json:"id"`
	Domain    string         `json:"domain"`
	Content   string         `json:"content"`
	Source    string         `json:"source,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Score     float64        `json:"score,omitempty"`
}

type BrowserTraceAPIStatus struct {
	TraceRuns       []BrowserTraceRun           `json:"trace_runs"`
	APICandidates   []BrowserTraceAPICandidate  `json:"api_candidates"`
	APISchemas      []BrowserTraceAPISchema     `json:"api_schemas"`
	APIValidations  []BrowserTraceAPIValidation `json:"api_validations"`
	CoverageReports []BrowserTraceAPICoverage   `json:"coverage_reports"`
	APIArtifacts    []BrowserTraceAPIArtifact   `json:"api_artifacts"`
}

type BrowserTraceRun struct {
	TraceRunID   string    `json:"trace_run_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	SiteID       string    `json:"site_id,omitempty"`
	Goal         string    `json:"goal,omitempty"`
	TracePath    string    `json:"trace_path"`
	CapturedAt   time.Time `json:"captured_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type BrowserTraceAPICandidate struct {
	CandidateID          string                      `json:"candidate_id"`
	TraceRunID           string                      `json:"trace_run_id"`
	SiteID               string                      `json:"site_id,omitempty"`
	Method               string                      `json:"method"`
	ObservedURL          string                      `json:"observed_url"`
	TemplatedURL         string                      `json:"templated_url,omitempty"`
	PathTemplate         string                      `json:"path_template,omitempty"`
	QueryParams          []BrowserTraceAPIQueryParam `json:"query_params,omitempty"`
	AuthRequired         bool                        `json:"auth_required"`
	ContainsPersonalData string                      `json:"contains_personal_data"`
	RiskLevel            string                      `json:"risk_level"`
	Status               string                      `json:"status"`
	Confidence           float64                     `json:"confidence,omitempty"`
	CreatedAt            time.Time                   `json:"created_at"`
}

type BrowserTraceAPIQueryParam struct {
	Name           string   `json:"name"`
	Type           string   `json:"type,omitempty"`
	ObservedValues []string `json:"observed_values,omitempty"`
}

type BrowserTraceAPISchema struct {
	SchemaID    string    `json:"schema_id"`
	CandidateID string    `json:"candidate_id"`
	SchemaType  string    `json:"schema_type"`
	SchemaJSON  string    `json:"schema_json"`
	SampleCount int       `json:"sample_count"`
	Confidence  float64   `json:"confidence,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type BrowserTraceAPIValidation struct {
	ValidationID string                           `json:"validation_id"`
	CandidateID  string                           `json:"candidate_id"`
	TraceRunID   string                           `json:"trace_run_id"`
	Passed       bool                             `json:"passed"`
	Status       string                           `json:"status"`
	Issues       []BrowserTraceAPIValidationIssue `json:"issues,omitempty"`
	CreatedAt    time.Time                        `json:"created_at"`
}

type BrowserTraceAPIValidationIssue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity,omitempty"`
}

type BrowserTraceAPICoverage struct {
	ReportID              string    `json:"report_id"`
	TraceRunID            string    `json:"trace_run_id"`
	ObservedFlows         []string  `json:"observed_flows,omitempty"`
	ObservedEndpoints     []string  `json:"observed_endpoints,omitempty"`
	MissingFlows          []string  `json:"missing_flows,omitempty"`
	RecommendedNextTraces []string  `json:"recommended_next_traces,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type BrowserTraceAPIArtifact struct {
	ArtifactID   string    `json:"artifact_id"`
	TraceRunID   string    `json:"trace_run_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	Type         string    `json:"artifact_type"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
}

type BrowserTraceAPIDiscoverRequest struct {
	TraceRunID      string    `json:"trace_run_id"`
	WorkstreamID    string    `json:"workstream_id,omitempty"`
	SiteID          string    `json:"site_id,omitempty"`
	Goal            string    `json:"goal,omitempty"`
	TracePath       string    `json:"trace_path"`
	RequestsPath    string    `json:"requests_path"`
	ResponsesPath   string    `json:"responses_path"`
	LivePolicyCheck bool      `json:"live_policy_check,omitempty"`
	CapturedAt      time.Time `json:"captured_at,omitempty"`
}

type BrowserTraceAPIDiscoverResponse struct {
	TraceRun       BrowserTraceRun             `json:"trace_run"`
	APICandidates  []BrowserTraceAPICandidate  `json:"api_candidates"`
	APISchemas     []BrowserTraceAPISchema     `json:"api_schemas"`
	APIValidations []BrowserTraceAPIValidation `json:"api_validations"`
	CoverageReport BrowserTraceAPICoverage     `json:"coverage_report"`
	APIArtifacts   []BrowserTraceAPIArtifact   `json:"api_artifacts"`
}

type BrowserTraceAPIFetcherProposalRequest struct {
	CandidateID   string `json:"candidate_id"`
	WorkstreamID  string `json:"workstream_id,omitempty"`
	HumanApproved bool   `json:"human_approved"`
}

type BrowserTraceAPIValidationReviewRequest struct {
	CandidateID         string `json:"candidate_id"`
	Reviewer            string `json:"reviewer"`
	ReviewNote          string `json:"review_note,omitempty"`
	HumanApproved       bool   `json:"human_approved"`
	TermsReviewed       bool   `json:"terms_reviewed"`
	OfficialAPIReviewed bool   `json:"official_api_reviewed"`
	PIIReviewed         bool   `json:"pii_reviewed"`
	SchemaReviewed      bool   `json:"schema_reviewed"`
	RiskReviewed        bool   `json:"risk_reviewed,omitempty"`
}

type BrowserTraceAPIValidationReviewResponse struct {
	Candidate           BrowserTraceAPICandidate  `json:"candidate"`
	Validation          BrowserTraceAPIValidation `json:"validation"`
	OfficialPromotion   bool                      `json:"official_promotion"`
	ImplementationApply bool                      `json:"implementation_apply"`
}

type BrowserTraceAPIFetcherProposalResponse struct {
	APIArtifact         BrowserTraceAPIArtifact   `json:"api_artifact"`
	WorkstreamArtifact  *WorkstreamArtifact       `json:"workstream_artifact,omitempty"`
	Candidate           BrowserTraceAPICandidate  `json:"candidate"`
	Validation          BrowserTraceAPIValidation `json:"validation"`
	OfficialPromotion   bool                      `json:"official_promotion"`
	ImplementationApply bool                      `json:"implementation_apply"`
}

type ContextBudgetDecision struct {
	Status           string  `json:"status"`
	Reason           string  `json:"reason"`
	ContextTokens    int     `json:"context_tokens"`
	MaxContextTokens int     `json:"max_context_tokens"`
	UsageRatio       float64 `json:"usage_ratio"`
}

type ContextBudgetResponse struct {
	ContextUsage ContextUsage          `json:"context_usage"`
	Decision     ContextBudgetDecision `json:"decision"`
	Event        *WorkflowEvent        `json:"event,omitempty"`
}

type HeavyWorkerRequest struct {
	EventID                     string `json:"event_id"`
	Agent                       string `json:"agent"`
	TargetFileCount             int    `json:"target_file_count,omitempty"`
	RelatedSpecCount            int    `json:"related_spec_count,omitempty"`
	CrossesArchitectureBoundary bool   `json:"crosses_architecture_boundary,omitempty"`
	HighUncertainty             bool   `json:"high_uncertainty,omitempty"`
	FailedAttempts              int    `json:"failed_attempts,omitempty"`
	UserRequestedDeepDive       bool   `json:"user_requested_deep_dive,omitempty"`
	Reason                      string `json:"reason,omitempty"`
}

type HeavyWorkerDecision struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons"`
}

type HeavyWorkerResponse struct {
	Request  HeavyWorkerRequest  `json:"request"`
	Decision HeavyWorkerDecision `json:"decision"`
	Event    *WorkflowEvent      `json:"event,omitempty"`
}

type HeavyWorkerRuntimeDiagnostics struct {
	Role           string                      `json:"role"`
	Route          string                      `json:"route"`
	RoutePrefix    string                      `json:"route_prefix"`
	Provider       string                      `json:"provider,omitempty"`
	Configured     bool                        `json:"configured"`
	BaseURL        string                      `json:"base_url,omitempty"`
	Model          string                      `json:"model,omitempty"`
	TimeoutSec     int                         `json:"timeout_sec,omitempty"`
	LLMOps         HeavyWorkerLLMOpsDiagnostic `json:"llm_ops"`
	FailureIsError bool                        `json:"failure_is_error"`
}

type HeavyWorkerLLMOpsDiagnostic struct {
	Configured    bool           `json:"configured"`
	Enabled       bool           `json:"enabled"`
	BaseURL       string         `json:"base_url,omitempty"`
	LiveAvailable bool           `json:"live_available"`
	RoleState     map[string]any `json:"role_state,omitempty"`
	Memory        map[string]any `json:"memory,omitempty"`
	Error         string         `json:"error,omitempty"`
}

type RunStateRequest struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

type RunStateResponse struct {
	RunID                 string `json:"run_id"`
	Status                string `json:"status"`
	EventID               string `json:"event_id"`
	RuntimeControlApplied bool   `json:"runtime_control_applied,omitempty"`
	RuntimeControlAction  string `json:"runtime_control_action,omitempty"`
}

type ExternalControlRequest struct {
	Actor         string `json:"actor"`
	ChannelID     string `json:"channel_id"`
	Action        string `json:"action"`
	HumanApproved bool   `json:"human_approved"`
}

type ExternalControlDecision struct {
	Status           string   `json:"status"`
	RequiresApproval bool     `json:"requires_approval"`
	Reasons          []string `json:"reasons,omitempty"`
}

type ExternalControlResponse struct {
	Request  ExternalControlRequest  `json:"request"`
	Decision ExternalControlDecision `json:"decision"`
}

type WorkstreamArtifact struct {
	ArtifactID   string    `json:"artifact_id"`
	WorkstreamID string    `json:"workstream_id"`
	Type         string    `json:"artifact_type"`
	FilePath     string    `json:"file_path,omitempty"`
	Title        string    `json:"title,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type WorkstreamStatus struct {
	Workstreams  []Workstream             `json:"workstreams"`
	Goals        []WorkstreamGoal         `json:"goals"`
	Artifacts    []WorkstreamArtifact     `json:"artifacts"`
	Annotations  []WorkstreamAnnotation   `json:"annotations"`
	Steering     []WorkstreamSteeringItem `json:"steering"`
	Heartbeats   []WorkstreamHeartbeat    `json:"heartbeats"`
	VaultUpdates []WorkstreamVaultUpdate  `json:"vault_updates"`
}

type Workstream struct {
	WorkstreamID string    `json:"workstream_id"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	Status       string    `json:"status"`
	PrimaryAgent string    `json:"primary_agent,omitempty"`
	VaultPath    string    `json:"vault_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

type WorkstreamGoal struct {
	GoalID          string    `json:"goal_id"`
	WorkstreamID    string    `json:"workstream_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	SuccessCriteria []string  `json:"success_criteria,omitempty"`
	Verification    []string  `json:"verification,omitempty"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

type WorkstreamAnnotation struct {
	AnnotationID string    `json:"annotation_id"`
	ArtifactID   string    `json:"artifact_id"`
	Target       string    `json:"target,omitempty"`
	Comment      string    `json:"comment"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ResolvedAt   time.Time `json:"resolved_at,omitempty"`
}

type WorkstreamSteeringItem struct {
	SteeringID       string    `json:"steering_id"`
	WorkstreamID     string    `json:"workstream_id"`
	TargetArtifactID string    `json:"target_artifact_id,omitempty"`
	Instruction      string    `json:"instruction"`
	Priority         string    `json:"priority,omitempty"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	AppliedAt        time.Time `json:"applied_at,omitempty"`
}

type WorkstreamHeartbeat struct {
	HeartbeatID  string    `json:"heartbeat_id"`
	WorkstreamID string    `json:"workstream_id"`
	ScheduleText string    `json:"schedule_text"`
	Task         string    `json:"task"`
	Status       string    `json:"status"`
	LastRunAt    time.Time `json:"last_run_at,omitempty"`
	NextRunAt    time.Time `json:"next_run_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type WorkstreamArtifactResponse struct {
	Artifact WorkstreamArtifact `json:"artifact"`
}

type WorkstreamVaultUpdate struct {
	UpdateID          string    `json:"update_id"`
	WorkstreamID      string    `json:"workstream_id"`
	FilePath          string    `json:"file_path"`
	UpdateType        string    `json:"update_type,omitempty"`
	ProposedContent   string    `json:"proposed_content,omitempty"`
	ContentHashBefore string    `json:"content_hash_before,omitempty"`
	ContentHashAfter  string    `json:"content_hash_after,omitempty"`
	ReviewStatus      string    `json:"review_status"`
	Applied           bool      `json:"applied,omitempty"`
	AppliedPath       string    `json:"applied_path,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type WorkstreamVaultUpdateResponse struct {
	VaultUpdate WorkstreamVaultUpdate `json:"vault_update"`
	Applied     bool                  `json:"applied,omitempty"`
	AppliedPath string                `json:"applied_path,omitempty"`
}

type WorkstreamVaultUpdatePreview struct {
	UpdateID        string `json:"update_id"`
	FilePath        string `json:"file_path"`
	CurrentContent  string `json:"current_content"`
	ProposedContent string `json:"proposed_content"`
	CurrentMissing  bool   `json:"current_missing"`
	AddedLines      int    `json:"added_lines"`
	RemovedLines    int    `json:"removed_lines"`
	UnifiedDiff     string `json:"unified_diff"`
}

type WorkstreamVaultUpdatePreviewResponse struct {
	Preview WorkstreamVaultUpdatePreview `json:"preview"`
}

type ComplexityStatus struct {
	Scans    []ComplexityScanEvent       `json:"scans"`
	Hotspots []ComplexityHotspot         `json:"hotspots"`
	Evidence []ComplexityHotspotEvidence `json:"evidence"`
	Reports  []ComplexityReportArtifact  `json:"reports"`
}

type ComplexityScanEvent struct {
	ScanID        string    `json:"scan_id"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	Repo          string    `json:"repo"`
	ScanScope     []string  `json:"scan_scope,omitempty"`
	Mode          string    `json:"mode"`
	FilesScanned  int       `json:"files_scanned"`
	HotspotsFound int       `json:"hotspots_found"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
}

type ComplexityHotspot struct {
	HotspotID            string    `json:"hotspot_id"`
	ScanID               string    `json:"scan_id"`
	FilePath             string    `json:"file_path"`
	LineStart            int       `json:"line_start,omitempty"`
	LineEnd              int       `json:"line_end,omitempty"`
	HotspotType          string    `json:"hotspot_type"`
	EstimatedComplexity  string    `json:"estimated_complexity"`
	EstimatedAfter       string    `json:"estimated_after,omitempty"`
	RiskLevel            string    `json:"risk_level"`
	PriorityScore        float64   `json:"priority_score,omitempty"`
	Confidence           float64   `json:"confidence,omitempty"`
	Summary              string    `json:"summary"`
	SuggestedImprovement string    `json:"suggested_improvement,omitempty"`
	RequiredTests        []string  `json:"required_tests,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type ComplexityHotspotEvidence struct {
	EvidenceID string    `json:"evidence_id"`
	HotspotID  string    `json:"hotspot_id"`
	FilePath   string    `json:"file_path"`
	LineStart  int       `json:"line_start,omitempty"`
	LineEnd    int       `json:"line_end,omitempty"`
	Snippet    string    `json:"snippet,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ComplexityReportArtifact struct {
	ArtifactID   string    `json:"artifact_id"`
	ScanID       string    `json:"scan_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	Type         string    `json:"artifact_type"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
}

type ComplexityConcreteDiffRequest struct {
	HotspotID                 string `json:"hotspot_id"`
	WorkstreamID              string `json:"workstream_id,omitempty"`
	ArtifactID                string `json:"artifact_id,omitempty"`
	PromotionID               string `json:"promotion_id,omitempty"`
	SandboxID                 string `json:"sandbox_id,omitempty"`
	TargetPath                string `json:"target_path,omitempty"`
	DiffPath                  string `json:"diff_path,omitempty"`
	ConcreteDiff              string `json:"concrete_diff"`
	TestResultPath            string `json:"test_result_path,omitempty"`
	RollbackPlanPath          string `json:"rollback_plan_path,omitempty"`
	PostApplyVerificationPath string `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string `json:"human_approval_status,omitempty"`
}

type ComplexityCoderDiffRequest struct {
	HotspotID                 string `json:"hotspot_id"`
	WorkstreamID              string `json:"workstream_id,omitempty"`
	JobID                     string `json:"job_id,omitempty"`
	ArtifactID                string `json:"artifact_id,omitempty"`
	PromotionID               string `json:"promotion_id,omitempty"`
	SandboxID                 string `json:"sandbox_id,omitempty"`
	TargetPath                string `json:"target_path,omitempty"`
	DiffPath                  string `json:"diff_path,omitempty"`
	TestResultPath            string `json:"test_result_path,omitempty"`
	RollbackPlanPath          string `json:"rollback_plan_path,omitempty"`
	PostApplyVerificationPath string `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string `json:"human_approval_status,omitempty"`
}

type ComplexityCoderDiffResult struct {
	JobID        string `json:"job_id"`
	Prompt       string `json:"prompt,omitempty"`
	RawResponse  string `json:"raw_response,omitempty"`
	ConcreteDiff string `json:"concrete_diff"`
}

type ComplexityDiffResponse struct {
	Hotspot               ComplexityHotspot          `json:"hotspot"`
	CoderResult           *ComplexityCoderDiffResult `json:"coder_result,omitempty"`
	ConcreteDiffArtifact  ComplexityReportArtifact   `json:"concrete_diff_artifact"`
	WorkstreamArtifact    *WorkstreamArtifact        `json:"workstream_artifact,omitempty"`
	HumanApprovalRequired bool                       `json:"human_approval_required"`
	PatchApplied          bool                       `json:"patch_applied"`
	SandboxPromotion      *PromotionRequest          `json:"sandbox_promotion,omitempty"`
	SandboxDecision       *PromotionGateDecision     `json:"sandbox_decision,omitempty"`
	SandboxGateLog        *PromotionGateLog          `json:"sandbox_gate_log,omitempty"`
}

type RevenueHumanDecision struct {
	DecisionID     string    `json:"decision_id,omitempty"`
	DecisionType   string    `json:"decision_type"`
	SubjectID      string    `json:"subject_id,omitempty"`
	Description    string    `json:"description,omitempty"`
	ApprovalStatus string    `json:"approval_status,omitempty"`
	CreatedAt      time.Time `json:"created_at,omitempty"`
}

type RevenueHumanDecisionResult struct {
	Status           string   `json:"status"`
	RequiresApproval bool     `json:"requires_approval"`
	Reasons          []string `json:"reasons,omitempty"`
}

type RevenueHumanDecisionRecord struct {
	DecisionID       string    `json:"decision_id"`
	DecisionType     string    `json:"decision_type"`
	SubjectID        string    `json:"subject_id,omitempty"`
	Description      string    `json:"description,omitempty"`
	ApprovalStatus   string    `json:"approval_status"`
	GateStatus       string    `json:"gate_status"`
	RequiresApproval bool      `json:"requires_approval"`
	Reasons          []string  `json:"reasons,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type RevenueHumanDecisionResponse struct {
	Decision RevenueHumanDecision       `json:"decision"`
	Record   RevenueHumanDecisionRecord `json:"record"`
	Result   RevenueHumanDecisionResult `json:"result"`
}

type RevenueStatus struct {
	HumanDecisions                       []RevenueHumanDecisionRecord     `json:"human_decisions"`
	DailyRoutineReports                  []RevenueDailyRoutineReport      `json:"daily_routine_reports"`
	ChannelDrafts                        []RevenueChannelDraft            `json:"channel_drafts"`
	ExternalSendApplyRecords             []RevenueExternalSendApplyRecord `json:"external_send_apply_records"`
	ExternalChannelAdapter               string                           `json:"external_channel_adapter"`
	ExternalChannelAdapterConfigured     *bool                            `json:"external_channel_adapter_configured"`
	HumanApprovalRequiredForExternalSend *bool                            `json:"human_approval_required_for_external_send"`
	Summary                              RevenueDashboardSummary          `json:"summary"`
}

type RevenueDashboardSummary struct {
	PendingDecisionCount   int  `json:"pending_decision_count"`
	DailyReportCount       int  `json:"daily_report_count"`
	ChannelDraftCount      int  `json:"channel_draft_count"`
	ExternalSendApplyCount int  `json:"external_send_apply_count"`
	ExternalActionsApplied bool `json:"external_actions_applied"`
}

type RevenueHumanDecisionReview struct {
	DecisionID     string `json:"decision_id"`
	ApprovalStatus string `json:"approval_status"`
}

type RevenueDailyRoutineRequest struct {
	ReportID     string `json:"report_id,omitempty"`
	WorkstreamID string `json:"workstream_id,omitempty"`
	Date         string `json:"date,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type RevenueDailyRoutineReport struct {
	ReportID            string    `json:"report_id"`
	WorkstreamID        string    `json:"workstream_id,omitempty"`
	Date                string    `json:"date"`
	Summary             string    `json:"summary,omitempty"`
	MarketResearch      int       `json:"market_research_count"`
	SNSPosts            int       `json:"sns_post_count"`
	Products            int       `json:"product_count"`
	CustomerVoices      int       `json:"customer_voice_count"`
	RevenueEvents       int       `json:"revenue_event_count"`
	PaidCustomers       int       `json:"paid_customer_count"`
	PendingDecisions    int       `json:"pending_decision_count"`
	SuggestedActions    []string  `json:"suggested_actions,omitempty"`
	Status              string    `json:"status"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at"`
}

type RevenueDailyRoutineResponse struct {
	Report                                  RevenueDailyRoutineReport `json:"daily_routine_report"`
	ExternalActionsApplied                  bool                      `json:"external_actions_applied"`
	HumanApprovalRequiredForExternalActions bool                      `json:"human_approval_required_for_external_actions"`
}

type RevenueChannelDraft struct {
	DraftID             string    `json:"draft_id"`
	WorkstreamID        string    `json:"workstream_id,omitempty"`
	Channel             string    `json:"channel"`
	Subject             string    `json:"subject,omitempty"`
	Body                string    `json:"body"`
	SourceReportID      string    `json:"source_report_id,omitempty"`
	ApprovalStatus      string    `json:"approval_status,omitempty"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at,omitempty"`
}

type RevenueChannelDraftResponse struct {
	Draft                                     RevenueChannelDraft `json:"channel_draft"`
	ExternalActionsApplied                    bool                `json:"external_actions_applied"`
	HumanApprovalRequiredForExternalSendApply bool                `json:"human_approval_required_for_external_send_apply"`
}

type RevenueExternalSendApplyRequest struct {
	ApplyID        string `json:"apply_id"`
	DraftID        string `json:"draft_id"`
	DecisionID     string `json:"decision_id"`
	Destination    string `json:"destination,omitempty"`
	ChannelAdapter string `json:"channel_adapter,omitempty"`
	HumanApproved  bool   `json:"human_approved"`
}

type RevenueExternalSendApplyRecord struct {
	ApplyID             string    `json:"apply_id"`
	DraftID             string    `json:"draft_id"`
	DecisionID          string    `json:"decision_id"`
	Channel             string    `json:"channel"`
	Destination         string    `json:"destination,omitempty"`
	ChannelAdapter      string    `json:"channel_adapter,omitempty"`
	ApprovalStatus      string    `json:"approval_status"`
	HumanApproved       bool      `json:"human_approved"`
	ApplyStatus         string    `json:"apply_status"`
	SendResult          string    `json:"send_result"`
	FailureReason       string    `json:"failure_reason,omitempty"`
	PostSendVerified    bool      `json:"post_send_verified"`
	PostSendEvidence    string    `json:"post_send_evidence,omitempty"`
	ExternalSendApplied bool      `json:"external_send_applied"`
	CreatedAt           time.Time `json:"created_at"`
}

type RevenueExternalSendApplyResponse struct {
	Record                              RevenueExternalSendApplyRecord `json:"external_send_apply_record"`
	ExternalActionsApplied              bool                           `json:"external_actions_applied"`
	PostSendVerified                    bool                           `json:"post_send_verified"`
	HumanApprovalRequiredForRetry       bool                           `json:"human_approval_required_for_retry"`
	ExternalChannelAdapterConfiguration string                         `json:"external_channel_adapter_configuration"`
	FailureReason                       string                         `json:"failure_reason"`
}

type SkillGovernanceExternalPRSubmitRequest struct {
	SubmitID            string `json:"submit_id"`
	ContributionEventID string `json:"contribution_event_id"`
	Repo                string `json:"repo"`
	TargetBranch        string `json:"target_branch,omitempty"`
	Title               string `json:"title,omitempty"`
	DiffPath            string `json:"diff_path,omitempty"`
	TestResult          string `json:"test_result,omitempty"`
	HumanApproved       bool   `json:"human_approved"`
}

type SkillGovernanceExternalPRSubmitRecord struct {
	SubmitID            string    `json:"submit_id"`
	ContributionEventID string    `json:"contribution_event_id"`
	Repo                string    `json:"repo"`
	TargetBranch        string    `json:"target_branch,omitempty"`
	Title               string    `json:"title,omitempty"`
	DiffPath            string    `json:"diff_path,omitempty"`
	TestResult          string    `json:"test_result,omitempty"`
	ApprovalStatus      string    `json:"approval_status"`
	HumanApproved       bool      `json:"human_approved"`
	SubmitStatus        string    `json:"submit_status"`
	PRURL               string    `json:"pr_url,omitempty"`
	FailureReason       string    `json:"failure_reason,omitempty"`
	ExternalPRCreated   bool      `json:"external_pr_created"`
	PostSubmitVerified  bool      `json:"post_submit_verified"`
	PostSubmitEvidence  string    `json:"post_submit_evidence,omitempty"`
	PRAdapter           string    `json:"pr_adapter,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type SkillGovernanceExternalPRSubmitResponse struct {
	Record                         SkillGovernanceExternalPRSubmitRecord `json:"external_pr_submit_record"`
	ExternalPRCreated              bool                                  `json:"external_pr_created"`
	PostSubmitVerified             bool                                  `json:"post_submit_verified"`
	HumanApprovalRequiredForPR     bool                                  `json:"human_approval_required_for_pr"`
	ExternalPRAdapterConfiguration string                                `json:"external_pr_adapter_configuration"`
	Message                        string                                `json:"message"`
}

type SkillGovernanceStatus struct {
	Manifests                   []SkillGovernanceManifest               `json:"manifests"`
	TriggerLogs                 []SkillGovernanceTriggerLog             `json:"trigger_logs"`
	ChangeLogs                  []SkillGovernanceChangeLog              `json:"change_logs"`
	Contributions               []SkillGovernanceContributionGateLog    `json:"contributions"`
	ExternalPRSubmitRecords     []SkillGovernanceExternalPRSubmitRecord `json:"external_pr_submit_records"`
	ExternalPRAdapter           string                                  `json:"external_pr_adapter"`
	ExternalPRAdapterConfigured *bool                                   `json:"external_pr_adapter_configured"`
	HumanApprovalRequiredForPR  *bool                                   `json:"human_approval_required_for_pr"`
	CoderTranscripts            []SkillGovernanceCoderTranscript        `json:"coder_transcripts"`
}

type SkillGovernanceManifest struct {
	SkillID string `json:"skill_id"`
	Name    string `json:"name"`
	Scope   string `json:"scope"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type SkillGovernanceTriggerLog struct {
	EventID     string    `json:"event_id"`
	SkillID     string    `json:"skill_id"`
	TriggerType string    `json:"trigger_type"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type SkillGovernanceChangeLog struct {
	ChangeID            string    `json:"change_id"`
	SkillID             string    `json:"skill_id"`
	EvalResult          string    `json:"eval_result,omitempty"`
	HumanApprovalStatus string    `json:"human_approval_status,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type SkillGovernanceCoderTranscript struct {
	EventID      string    `json:"event_id"`
	JobID        string    `json:"job_id,omitempty"`
	SessionID    string    `json:"session_id,omitempty"`
	Route        string    `json:"route,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Role         string    `json:"role"`
	Segment      string    `json:"segment"`
	Text         string    `json:"text,omitempty"`
	EvidencePath string    `json:"evidence_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type SkillGovernanceContributionGateRequest struct {
	EventID             string `json:"event_id,omitempty"`
	Repo                string `json:"repo"`
	TargetBranch        string `json:"target_branch,omitempty"`
	ProblemStatement    string `json:"problem_statement,omitempty"`
	ExistingPRsChecked  bool   `json:"existing_prs_checked"`
	RealProblemVerified bool   `json:"real_problem_verified"`
	CoreChangeVerified  bool   `json:"core_change_verified"`
	DiffHumanApproved   bool   `json:"diff_human_approved"`
	TestResult          string `json:"test_result,omitempty"`
}

type SkillGovernanceContributionGateLog struct {
	EventID             string    `json:"event_id"`
	Repo                string    `json:"repo"`
	TargetBranch        string    `json:"target_branch,omitempty"`
	ProblemStatement    string    `json:"problem_statement,omitempty"`
	ExistingPRsChecked  bool      `json:"existing_prs_checked"`
	RealProblemVerified bool      `json:"real_problem_verified"`
	CoreChangeVerified  bool      `json:"core_change_verified"`
	DiffHumanApproved   bool      `json:"diff_human_approved"`
	TestResult          string    `json:"test_result,omitempty"`
	GateStatus          string    `json:"gate_status"`
	CreatedAt           time.Time `json:"created_at"`
}

type SkillGovernanceContributionGateDecision struct {
	Status        string   `json:"status"`
	StopReasons   []string `json:"stop_reasons,omitempty"`
	NextActions   []string `json:"next_actions,omitempty"`
	CanContribute bool     `json:"can_contribute"`
}

type SkillGovernanceContributionGateResponse struct {
	GateLog  SkillGovernanceContributionGateLog      `json:"gate_log"`
	Decision SkillGovernanceContributionGateDecision `json:"decision"`
}

type SandboxStatus struct {
	Sandboxes  []SandboxRecord         `json:"sandboxes"`
	Artifacts  []SandboxArtifact       `json:"artifacts"`
	Promotions []PromotionRequest      `json:"promotions"`
	Decisions  []PromotionGateDecision `json:"decisions"`
	GateLogs   []PromotionGateLog      `json:"gate_logs"`
}

type SandboxRecord struct {
	SandboxID    string    `json:"sandbox_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	GoalID       string    `json:"goal_id,omitempty"`
	Type         string    `json:"type"`
	Path         string    `json:"path"`
	BaseRef      string    `json:"base_ref,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	ClosedAt     time.Time `json:"closed_at,omitempty"`
}

type SandboxArtifact struct {
	ArtifactID string    `json:"artifact_id"`
	SandboxID  string    `json:"sandbox_id"`
	Type       string    `json:"artifact_type"`
	FilePath   string    `json:"file_path"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

type PromotionRequest struct {
	PromotionID               string    `json:"promotion_id"`
	SandboxID                 string    `json:"sandbox_id"`
	WorkstreamID              string    `json:"workstream_id,omitempty"`
	GoalID                    string    `json:"goal_id,omitempty"`
	RequestedBy               string    `json:"requested_by,omitempty"`
	TargetPath                string    `json:"target_path"`
	DiffPath                  string    `json:"diff_path"`
	TestResultPath            string    `json:"test_result_path"`
	RiskLevel                 string    `json:"risk_level,omitempty"`
	Reason                    string    `json:"reason"`
	RollbackPlanPath          string    `json:"rollback_plan_path"`
	PostApplyVerificationPath string    `json:"post_apply_verification_path,omitempty"`
	HumanApprovalStatus       string    `json:"human_approval_status"`
	CreatedAt                 time.Time `json:"created_at"`
}

type PromotionGateDecision struct {
	Status              string   `json:"status"`
	Reason              string   `json:"reason"`
	MissingRequirements []string `json:"missing_requirements,omitempty"`
}

type PromotionGateLog struct {
	EventID               string    `json:"event_id"`
	PromotionID           string    `json:"promotion_id"`
	GateStatus            string    `json:"gate_status"`
	Reason                string    `json:"reason"`
	HumanApprovalStatus   string    `json:"human_approval_status"`
	PostApplyVerification string    `json:"post_apply_verification,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type PromotionRequestResponse struct {
	Promotion                     PromotionRequest      `json:"promotion"`
	Decision                      PromotionGateDecision `json:"decision"`
	GateLog                       PromotionGateLog      `json:"gate_log"`
	RollbackArtifact              *SandboxArtifact      `json:"rollback_artifact,omitempty"`
	PostApplyVerificationArtifact *SandboxArtifact      `json:"post_apply_verification_artifact,omitempty"`
}

type PromotionApplyRequest struct {
	Promotion                    PromotionRequest `json:"promotion"`
	AppliedBy                    string           `json:"applied_by,omitempty"`
	ApplyTarget                  string           `json:"apply_target,omitempty"`
	PostApplyVerificationPath    string           `json:"post_apply_verification_path"`
	PostApplyVerificationCommand string           `json:"post_apply_verification_command,omitempty"`
	HumanApproved                bool             `json:"human_approved"`
}

type PromotionDiffApplyResult struct {
	DiffPath     string   `json:"diff_path"`
	ApplyRoot    string   `json:"apply_root"`
	AppliedFiles []string `json:"applied_files"`
	Status       string   `json:"status"`
}

type PromotionApplyResponse struct {
	Decision                      PromotionGateDecision     `json:"decision"`
	DiffApplyResult               *PromotionDiffApplyResult `json:"diff_apply_result,omitempty"`
	GateLog                       PromotionGateLog          `json:"gate_log"`
	PostApplyVerificationArtifact SandboxArtifact           `json:"post_apply_verification_artifact"`
}

type PromotionRollbackResponse struct {
	Decision         PromotionGateDecision    `json:"decision"`
	RollbackResult   PromotionDiffApplyResult `json:"rollback_result"`
	RollbackArtifact SandboxArtifact          `json:"rollback_artifact"`
	GateLog          PromotionGateLog         `json:"gate_log"`
}

type PromotionWorkflowRequest struct {
	Promotion                    PromotionRequest        `json:"promotion"`
	ApplyAfterApproval           bool                    `json:"apply_after_approval,omitempty"`
	AppliedBy                    string                  `json:"applied_by,omitempty"`
	ApplyTarget                  string                  `json:"apply_target,omitempty"`
	PostApplyVerificationPath    string                  `json:"post_apply_verification_path,omitempty"`
	PostApplyVerificationCommand string                  `json:"post_apply_verification_command,omitempty"`
	HumanApproved                bool                    `json:"human_approved,omitempty"`
	ExternalControl              *ExternalControlRequest `json:"external_control,omitempty"`
}

type PromotionWorkflowResponse struct {
	PromotionResponse PromotionRequestResponse `json:"promotion_response"`
	ApplyResponse     *PromotionApplyResponse  `json:"apply_response,omitempty"`
	Applied           bool                     `json:"applied"`
	SkippedReason     string                   `json:"skipped_reason,omitempty"`
}

func (c *Client) SuperAgentStatus(ctx context.Context, limit int) (SuperAgentStatus, error) {
	path := "/viewer/superagent"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out SuperAgentStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SuperAgentStatus{}, err
	}
	if err := validateSuperAgentStatus(out); err != nil {
		return SuperAgentStatus{}, err
	}
	return out, nil
}

func (c *Client) RuntimeConfig(ctx context.Context) (RuntimeConfig, error) {
	var out RuntimeConfig
	if err := c.do(ctx, http.MethodGet, "/viewer/runtime-config", nil, &out); err != nil {
		return RuntimeConfig{}, err
	}
	if err := validateRuntimeConfig(out); err != nil {
		return RuntimeConfig{}, err
	}
	return out, nil
}

func (c *Client) RuntimeHealth(ctx context.Context) (RuntimeHealthReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return RuntimeHealthReport{}, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return RuntimeHealthReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return RuntimeHealthReport{}, &APIError{Method: http.MethodGet, Path: "/health", StatusCode: resp.StatusCode, Body: string(data)}
	}
	var out RuntimeHealthReport
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return RuntimeHealthReport{}, fmt.Errorf("decode response: %w", err)
	}
	if err := validateRuntimeHealth(out, resp.StatusCode); err != nil {
		return RuntimeHealthReport{}, err
	}
	return out, nil
}

func (c *Client) LLMOpsStatus(ctx context.Context) (LLMOpsStatus, error) {
	var out LLMOpsStatus
	if err := c.do(ctx, http.MethodGet, "/viewer/llm-ops/status", nil, &out); err != nil {
		return LLMOpsStatus{}, err
	}
	if err := validateLLMOpsStatus(out); err != nil {
		return LLMOpsStatus{}, err
	}
	return out, nil
}

func (c *Client) LLMOpsHealth(ctx context.Context) (LLMOpsHealth, error) {
	var out LLMOpsHealth
	if err := c.do(ctx, http.MethodGet, "/viewer/llm-ops/health", nil, &out); err != nil {
		return LLMOpsHealth{}, err
	}
	if err := validateLLMOpsHealth(out); err != nil {
		return LLMOpsHealth{}, err
	}
	return out, nil
}

func (c *Client) StopLLMOps(ctx context.Context, roles []string) error {
	normalized, err := normalizeLLMOpsRoles(roles)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/llm-ops/stop", llmOpsStopRequest{Roles: normalized}, nil)
}

func (c *Client) StartLLMOps(ctx context.Context, selection string) error {
	selection, err := normalizeLLMOpsSelection(selection, false)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/llm-ops/start", llmOpsSelectionRequest{Selection: selection}, nil)
}

func (c *Client) RestartLLMOps(ctx context.Context, selection string) error {
	selection, err := normalizeLLMOpsSelection(selection, true)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/llm-ops/restart", llmOpsSelectionRequest{Selection: selection}, nil)
}

func (c *Client) DebugSystemSnapshot(ctx context.Context) (DebugSystemSnapshot, error) {
	var out DebugSystemSnapshot
	if err := c.do(ctx, http.MethodGet, "/viewer/debug/system", nil, &out); err != nil {
		return DebugSystemSnapshot{}, err
	}
	if err := validateDebugSystemSnapshot(out); err != nil {
		return DebugSystemSnapshot{}, err
	}
	return out, nil
}

func (c *Client) CreateAgentRun(ctx context.Context, item AgentRun) error {
	if err := validateAgentRunRequest(item); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/superagent/runs", item, nil)
}

func (c *Client) CreateTraceEvent(ctx context.Context, item TraceEvent) error {
	if err := validateTraceEventRequest(item); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/superagent/trace-events", item, nil)
}

func (c *Client) CreateRunQueueItem(ctx context.Context, item RunQueueItem) error {
	item = normalizeRunQueueCreateRequest(item)
	if err := validateRunQueueCreateRequest(item); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/viewer/superagent/run-queue", item, nil)
}

func (c *Client) ClaimRunQueueItem(ctx context.Context) (RunQueueClaimResponse, error) {
	var out RunQueueClaimResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/superagent/run-queue/claim", nil, &out); err != nil {
		return RunQueueClaimResponse{}, err
	}
	if err := validateRunQueueClaimResponse(out); err != nil {
		return RunQueueClaimResponse{}, err
	}
	return out, nil
}

func (c *Client) CompleteRunQueueItem(ctx context.Context, req RunQueueCompleteRequest) (RunQueueCompleteResponse, error) {
	req = normalizeRunQueueCompleteRequest(req)
	if err := validateRunQueueCompleteRequest(req); err != nil {
		return RunQueueCompleteResponse{}, err
	}
	var out RunQueueCompleteResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/superagent/run-queue/complete", req, &out); err != nil {
		return RunQueueCompleteResponse{}, err
	}
	if err := validateRunQueueCompleteResponse(out, req); err != nil {
		return RunQueueCompleteResponse{}, err
	}
	return out, nil
}

func (c *Client) PauseRun(ctx context.Context, runID string, reason string) (RunStateResponse, error) {
	if strings.TrimSpace(runID) == "" {
		return RunStateResponse{}, fmt.Errorf("run state request missing run_id")
	}
	var out RunStateResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/superagent/runs/pause", RunStateRequest{RunID: runID, Reason: reason}, &out); err != nil {
		return RunStateResponse{}, err
	}
	if err := validateRunStateResponse(out, runID, "paused", map[string]bool{"none": true, "cancel_requested": true}); err != nil {
		return RunStateResponse{}, err
	}
	return out, nil
}

func (c *Client) ResumeRun(ctx context.Context, runID string, reason string) (RunStateResponse, error) {
	if strings.TrimSpace(runID) == "" {
		return RunStateResponse{}, fmt.Errorf("run state request missing run_id")
	}
	var out RunStateResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/superagent/runs/resume", RunStateRequest{RunID: runID, Reason: reason}, &out); err != nil {
		return RunStateResponse{}, err
	}
	if err := validateRunStateResponse(out, runID, "running", map[string]bool{"none": true, "resume_marker_cleared": true}); err != nil {
		return RunStateResponse{}, err
	}
	return out, nil
}

func (c *Client) CheckExternalControl(ctx context.Context, req ExternalControlRequest) (ExternalControlResponse, error) {
	if err := validateExternalControlRequest(req); err != nil {
		return ExternalControlResponse{}, err
	}
	var out ExternalControlResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/ai-workflow/external-control/check", req, &out); err != nil {
		return ExternalControlResponse{}, err
	}
	if err := validateExternalControlResponse(out, req); err != nil {
		return ExternalControlResponse{}, err
	}
	return out, nil
}

func (c *Client) EvaluateHeavyWorker(ctx context.Context, req HeavyWorkerRequest) (HeavyWorkerResponse, error) {
	if err := validateHeavyWorkerRequest(req); err != nil {
		return HeavyWorkerResponse{}, err
	}
	var out HeavyWorkerResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/ai-workflow/heavy-worker/evaluate", req, &out); err != nil {
		return HeavyWorkerResponse{}, err
	}
	if err := validateHeavyWorkerResponse(out, req); err != nil {
		return HeavyWorkerResponse{}, err
	}
	return out, nil
}

func (c *Client) HeavyWorkerRuntimeDiagnostics(ctx context.Context) (HeavyWorkerRuntimeDiagnostics, error) {
	var out HeavyWorkerRuntimeDiagnostics
	if err := c.do(ctx, http.MethodGet, "/viewer/ai-workflow/heavy-worker/runtime-diagnostics", nil, &out); err != nil {
		return HeavyWorkerRuntimeDiagnostics{}, err
	}
	if err := validateHeavyWorkerRuntimeDiagnostics(out); err != nil {
		return HeavyWorkerRuntimeDiagnostics{}, err
	}
	return out, nil
}

func (c *Client) AIWorkflowStatus(ctx context.Context, limit int) (AIWorkflowStatus, error) {
	path := "/viewer/ai-workflow"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out AIWorkflowStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return AIWorkflowStatus{}, err
	}
	if err := validateAIWorkflowStatus(out); err != nil {
		return AIWorkflowStatus{}, err
	}
	return out, nil
}

func (c *Client) ToolHarnessStatus(ctx context.Context, limit int) (ToolHarnessStatus, error) {
	path := "/viewer/tool-harness/recent"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out ToolHarnessStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return ToolHarnessStatus{}, err
	}
	if err := validateToolHarnessStatus(out); err != nil {
		return ToolHarnessStatus{}, err
	}
	return out, nil
}

func (c *Client) DCIRecent(ctx context.Context, limit int) (DCIRecentStatus, error) {
	path := "/viewer/dci/recent"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out DCIRecentStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return DCIRecentStatus{}, err
	}
	if err := validateDCIRecentStatus(out); err != nil {
		return DCIRecentStatus{}, err
	}
	return out, nil
}

func (c *Client) DCISearch(ctx context.Context, req DCISearchRequest) (DCISearchResult, error) {
	if err := validateDCISearchRequest(req); err != nil {
		return DCISearchResult{}, err
	}
	var out DCISearchResult
	if err := c.do(ctx, http.MethodPost, "/viewer/dci/search", req, &out); err != nil {
		return DCISearchResult{}, err
	}
	if err := validateDCISearchResult(out, req); err != nil {
		return DCISearchResult{}, err
	}
	return out, nil
}

func (c *Client) KnowledgeMemoryStatus(ctx context.Context, limit int) (KnowledgeMemoryStatus, error) {
	path := "/viewer/knowledge-memory"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out KnowledgeMemoryStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return KnowledgeMemoryStatus{}, err
	}
	if err := validateKnowledgeMemoryStatus(out); err != nil {
		return KnowledgeMemoryStatus{}, err
	}
	return out, nil
}

func (c *Client) CreateKnowledgeNewsItem(ctx context.Context, item KnowledgeNewsItem) (KnowledgeMemoryCreateResponse, error) {
	if err := validateKnowledgeNewsItem(item); err != nil {
		return KnowledgeMemoryCreateResponse{}, err
	}
	return c.createKnowledgeMemoryItem(ctx, "/viewer/knowledge-memory/news-knowledge", item)
}

func (c *Client) CreateKnowledgeDailyIntakeRule(ctx context.Context, item KnowledgeDailyIntakeRule) (KnowledgeMemoryCreateResponse, error) {
	if err := validateKnowledgeDailyIntakeRule(item); err != nil {
		return KnowledgeMemoryCreateResponse{}, err
	}
	return c.createKnowledgeMemoryItem(ctx, "/viewer/knowledge-memory/daily-intake-rules", item)
}

func (c *Client) createKnowledgeMemoryItem(ctx context.Context, path string, item any) (KnowledgeMemoryCreateResponse, error) {
	var out KnowledgeMemoryCreateResponse
	if err := c.do(ctx, http.MethodPost, path, item, &out); err != nil {
		return KnowledgeMemoryCreateResponse{}, err
	}
	if err := validateKnowledgeMemoryCreateResponse(out); err != nil {
		return KnowledgeMemoryCreateResponse{}, err
	}
	return out, nil
}

func (c *Client) ReviewKnowledgeMemory(ctx context.Context, req KnowledgeMemoryReviewRequest) (KnowledgeMemoryReviewResponse, error) {
	if err := validateKnowledgeMemoryReviewRequest(req); err != nil {
		return KnowledgeMemoryReviewResponse{}, err
	}
	var out KnowledgeMemoryReviewResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/knowledge-memory/review", req, &out); err != nil {
		return KnowledgeMemoryReviewResponse{}, err
	}
	if err := validateKnowledgeMemoryReviewResponse(out, req); err != nil {
		return KnowledgeMemoryReviewResponse{}, err
	}
	return out, nil
}

func (c *Client) SourceRegistryStatus(ctx context.Context, enabledOnly bool) (SourceRegistryStatus, error) {
	path := "/viewer/source-registry"
	if enabledOnly {
		path += "?enabled_only=true"
	}
	var out SourceRegistryStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SourceRegistryStatus{}, err
	}
	if err := validateSourceRegistryStatus(out); err != nil {
		return SourceRegistryStatus{}, err
	}
	return out, nil
}

func (c *Client) SourceRegistryStaging(ctx context.Context, status string, limit int) (SourceRegistryStagingStatus, error) {
	values := url.Values{}
	values.Set("action", "staging")
	if strings.TrimSpace(status) != "" {
		values.Set("status", strings.TrimSpace(status))
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	path := "/viewer/source-registry?" + values.Encode()
	var out SourceRegistryStagingStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SourceRegistryStagingStatus{}, err
	}
	if err := validateSourceRegistryStagingStatus(out); err != nil {
		return SourceRegistryStagingStatus{}, err
	}
	return out, nil
}

func (c *Client) ValidateSourceRegistryStaging(ctx context.Context, req SourceRegistryValidateRequest) (SourceRegistryValidationResponse, error) {
	if err := validateSourceRegistryValidateRequest(req); err != nil {
		return SourceRegistryValidationResponse{}, err
	}
	var out SourceRegistryValidationResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/source-registry?action=validate", req, &out); err != nil {
		return SourceRegistryValidationResponse{}, err
	}
	if err := validateSourceRegistryValidationResponse(out, req); err != nil {
		return SourceRegistryValidationResponse{}, err
	}
	return out, nil
}

func (c *Client) PromoteSourceRegistryStaging(ctx context.Context, req SourceRegistryPromoteRequest) (SourceRegistryPromotionResponse, error) {
	if err := validateSourceRegistryPromoteRequest(req); err != nil {
		return SourceRegistryPromotionResponse{}, err
	}
	var out SourceRegistryPromotionResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/source-registry?action=promote", req, &out); err != nil {
		return SourceRegistryPromotionResponse{}, err
	}
	if err := validateSourceRegistryPromotionResponse(out, req); err != nil {
		return SourceRegistryPromotionResponse{}, err
	}
	return out, nil
}

func (c *Client) DomainGraphAssertions(ctx context.Context, req DomainGraphAssertionsRequest) (DomainGraphAssertionsResponse, error) {
	values := url.Values{}
	if strings.TrimSpace(req.Domain) != "" {
		values.Set("domain", strings.TrimSpace(req.Domain))
	}
	if strings.TrimSpace(req.EntityType) != "" {
		values.Set("entity_type", strings.TrimSpace(req.EntityType))
	}
	if strings.TrimSpace(req.EntityID) != "" {
		values.Set("entity_id", strings.TrimSpace(req.EntityID))
	}
	if strings.TrimSpace(req.RelationType) != "" {
		values.Set("relation_type", strings.TrimSpace(req.RelationType))
	}
	if strings.TrimSpace(req.SourceID) != "" {
		values.Set("source_id", strings.TrimSpace(req.SourceID))
	}
	if strings.TrimSpace(req.ValidationStatus) != "" {
		values.Set("validation_status", strings.TrimSpace(req.ValidationStatus))
	}
	if req.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", req.Limit))
	}
	if req.Offset > 0 {
		values.Set("offset", fmt.Sprintf("%d", req.Offset))
	}
	path := "/viewer/domain-graph/assertions"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out DomainGraphAssertionsResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return DomainGraphAssertionsResponse{}, err
	}
	if err := validateDomainGraphAssertionsResponse(out); err != nil {
		return DomainGraphAssertionsResponse{}, err
	}
	return out, nil
}

func (c *Client) MemoryLayers(ctx context.Context, req MemoryLayersRequest) (MemoryLayersStatus, error) {
	values := url.Values{}
	if strings.TrimSpace(req.SessionID) != "" {
		values.Set("session_id", strings.TrimSpace(req.SessionID))
	}
	if strings.TrimSpace(req.Namespace) != "" {
		values.Set("namespace", strings.TrimSpace(req.Namespace))
	}
	if strings.TrimSpace(req.Domain) != "" {
		values.Set("domain", strings.TrimSpace(req.Domain))
	}
	if req.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", req.Limit))
	}
	path := "/viewer/memory/layers"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out MemoryLayersStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return MemoryLayersStatus{}, err
	}
	if err := validateMemoryLayersStatus(out); err != nil {
		return MemoryLayersStatus{}, err
	}
	return out, nil
}

func (c *Client) BrowserTraceAPIStatus(ctx context.Context, limit int) (BrowserTraceAPIStatus, error) {
	path := "/viewer/browser-trace-api"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out BrowserTraceAPIStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return BrowserTraceAPIStatus{}, err
	}
	if err := validateBrowserTraceAPIStatus(out); err != nil {
		return BrowserTraceAPIStatus{}, err
	}
	return out, nil
}

func (c *Client) DiscoverBrowserTraceAPI(ctx context.Context, req BrowserTraceAPIDiscoverRequest) (BrowserTraceAPIDiscoverResponse, error) {
	if err := validateBrowserTraceAPIDiscoverRequest(req); err != nil {
		return BrowserTraceAPIDiscoverResponse{}, err
	}
	var out BrowserTraceAPIDiscoverResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/browser-trace-api/discover", req, &out); err != nil {
		return BrowserTraceAPIDiscoverResponse{}, err
	}
	if err := validateBrowserTraceAPIDiscoverResponse(out, req); err != nil {
		return BrowserTraceAPIDiscoverResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateBrowserTraceAPIFetcherProposal(ctx context.Context, req BrowserTraceAPIFetcherProposalRequest) (BrowserTraceAPIFetcherProposalResponse, error) {
	if err := validateBrowserTraceAPIFetcherProposalRequest(req); err != nil {
		return BrowserTraceAPIFetcherProposalResponse{}, err
	}
	var out BrowserTraceAPIFetcherProposalResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", req, &out); err != nil {
		return BrowserTraceAPIFetcherProposalResponse{}, err
	}
	if err := validateBrowserTraceAPIFetcherProposalResponse(out, req); err != nil {
		return BrowserTraceAPIFetcherProposalResponse{}, err
	}
	return out, nil
}

func (c *Client) ValidateBrowserTraceAPICandidate(ctx context.Context, req BrowserTraceAPIValidationReviewRequest) (BrowserTraceAPIValidationReviewResponse, error) {
	if err := validateBrowserTraceAPIValidationReviewRequest(req); err != nil {
		return BrowserTraceAPIValidationReviewResponse{}, err
	}
	var out BrowserTraceAPIValidationReviewResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/browser-trace-api/validations", req, &out); err != nil {
		return BrowserTraceAPIValidationReviewResponse{}, err
	}
	if err := validateBrowserTraceAPIValidationReviewResponse(out, req); err != nil {
		return BrowserTraceAPIValidationReviewResponse{}, err
	}
	return out, nil
}

func (c *Client) RunCommand(ctx context.Context, req CommandRunRequest) (CommandRunResponse, error) {
	if err := validateCommandRunRequest(req); err != nil {
		return CommandRunResponse{}, err
	}
	var out CommandRunResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/ai-workflow/commands/run", req, &out); err != nil {
		return CommandRunResponse{}, err
	}
	if err := validateCommandRunResponse(out, req); err != nil {
		return CommandRunResponse{}, err
	}
	return out, nil
}

func (c *Client) CheckContextBudget(ctx context.Context, usage ContextUsage) (ContextBudgetResponse, error) {
	if err := validateContextUsageRequest(usage); err != nil {
		return ContextBudgetResponse{}, err
	}
	var out ContextBudgetResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/ai-workflow/context-budget/check", usage, &out); err != nil {
		return ContextBudgetResponse{}, err
	}
	if err := validateContextBudgetResponse(out, usage); err != nil {
		return ContextBudgetResponse{}, err
	}
	return out, nil
}

func (c *Client) WorkstreamStatus(ctx context.Context, limit int) (WorkstreamStatus, error) {
	path := "/viewer/workstreams"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out WorkstreamStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return WorkstreamStatus{}, err
	}
	if err := validateWorkstreamStatus(out); err != nil {
		return WorkstreamStatus{}, err
	}
	return out, nil
}

func (c *Client) CreateWorkstreamArtifact(ctx context.Context, item WorkstreamArtifact) (WorkstreamArtifactResponse, error) {
	if err := validateWorkstreamArtifactRequest(item); err != nil {
		return WorkstreamArtifactResponse{}, err
	}
	var out WorkstreamArtifactResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/workstreams/artifacts", item, &out); err != nil {
		return WorkstreamArtifactResponse{}, err
	}
	if err := validateWorkstreamArtifactResponse(out, item); err != nil {
		return WorkstreamArtifactResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateWorkstreamVaultUpdate(ctx context.Context, item WorkstreamVaultUpdate) (WorkstreamVaultUpdateResponse, error) {
	if strings.TrimSpace(item.ReviewStatus) == "" {
		item.ReviewStatus = "pending"
	}
	if err := validateWorkstreamVaultUpdateRequest(item); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	var out WorkstreamVaultUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/workstreams/vault-updates", item, &out); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	if err := validateWorkstreamVaultUpdateCreateResponse(out, item); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	return out, nil
}

func (c *Client) PreviewWorkstreamVaultUpdate(ctx context.Context, item WorkstreamVaultUpdate) (WorkstreamVaultUpdatePreviewResponse, error) {
	if err := validateWorkstreamVaultUpdateRequest(item); err != nil {
		return WorkstreamVaultUpdatePreviewResponse{}, err
	}
	var out WorkstreamVaultUpdatePreviewResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/workstreams/vault-updates/preview", item, &out); err != nil {
		return WorkstreamVaultUpdatePreviewResponse{}, err
	}
	if err := validateWorkstreamVaultUpdatePreviewResponse(out, item); err != nil {
		return WorkstreamVaultUpdatePreviewResponse{}, err
	}
	return out, nil
}

func (c *Client) ReviewWorkstreamVaultUpdate(ctx context.Context, item WorkstreamVaultUpdate) (WorkstreamVaultUpdateResponse, error) {
	if err := validateWorkstreamVaultReviewRequest(item); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	var out WorkstreamVaultUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/workstreams/vault-updates/review", item, &out); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	if err := validateWorkstreamVaultUpdateReviewResponse(out, item); err != nil {
		return WorkstreamVaultUpdateResponse{}, err
	}
	return out, nil
}

func (c *Client) ComplexityStatus(ctx context.Context, limit int) (ComplexityStatus, error) {
	path := "/viewer/complexity-hotspots"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out ComplexityStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return ComplexityStatus{}, err
	}
	if err := validateComplexityStatus(out); err != nil {
		return ComplexityStatus{}, err
	}
	return out, nil
}

func (c *Client) CreateComplexityConcreteDiff(ctx context.Context, req ComplexityConcreteDiffRequest) (ComplexityDiffResponse, error) {
	if err := validateComplexityConcreteDiffRequest(req); err != nil {
		return ComplexityDiffResponse{}, err
	}
	var out ComplexityDiffResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/complexity-hotspots/concrete-diffs", req, &out); err != nil {
		return ComplexityDiffResponse{}, err
	}
	if err := validateComplexityDiffResponse(out, req, ComplexityCoderDiffRequest{}, false); err != nil {
		return ComplexityDiffResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateComplexityCoderDiff(ctx context.Context, req ComplexityCoderDiffRequest) (ComplexityDiffResponse, error) {
	if err := validateComplexityCoderDiffRequest(req); err != nil {
		return ComplexityDiffResponse{}, err
	}
	var out ComplexityDiffResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/complexity-hotspots/coder-diffs", req, &out); err != nil {
		return ComplexityDiffResponse{}, err
	}
	if err := validateComplexityDiffResponse(out, ComplexityConcreteDiffRequest{
		HotspotID:                 req.HotspotID,
		WorkstreamID:              req.WorkstreamID,
		ArtifactID:                req.ArtifactID,
		PromotionID:               req.PromotionID,
		SandboxID:                 req.SandboxID,
		TargetPath:                req.TargetPath,
		DiffPath:                  req.DiffPath,
		TestResultPath:            req.TestResultPath,
		RollbackPlanPath:          req.RollbackPlanPath,
		PostApplyVerificationPath: req.PostApplyVerificationPath,
		HumanApprovalStatus:       req.HumanApprovalStatus,
	}, req, true); err != nil {
		return ComplexityDiffResponse{}, err
	}
	return out, nil
}

func (c *Client) RevenueStatus(ctx context.Context, limit int) (RevenueStatus, error) {
	path := "/viewer/revenue"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out RevenueStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return RevenueStatus{}, err
	}
	if err := validateRevenueStatus(out); err != nil {
		return RevenueStatus{}, err
	}
	return out, nil
}

func (c *Client) EvaluateRevenueHumanDecision(ctx context.Context, item RevenueHumanDecision) (RevenueHumanDecisionResponse, error) {
	if err := validateRevenueHumanDecisionRequest(item); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	var out RevenueHumanDecisionResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/revenue/human-decision-gate", item, &out); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	if err := validateRevenueHumanDecisionResponse(out, item, ""); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	return out, nil
}

func (c *Client) ReviewRevenueHumanDecision(ctx context.Context, item RevenueHumanDecisionReview) (RevenueHumanDecisionResponse, error) {
	if err := validateRevenueHumanDecisionReviewRequest(item); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	var out RevenueHumanDecisionResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/revenue/human-decision-gate/review", item, &out); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	if err := validateRevenueHumanDecisionResponse(out, RevenueHumanDecision{DecisionID: item.DecisionID}, item.ApprovalStatus); err != nil {
		return RevenueHumanDecisionResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateRevenueDailyRoutineReport(ctx context.Context, item RevenueDailyRoutineRequest) (RevenueDailyRoutineResponse, error) {
	if err := validateRevenueDailyRoutineRequest(item); err != nil {
		return RevenueDailyRoutineResponse{}, err
	}
	var out RevenueDailyRoutineResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/revenue/daily-routine", item, &out); err != nil {
		return RevenueDailyRoutineResponse{}, err
	}
	if err := validateRevenueDailyRoutineResponse(out, item); err != nil {
		return RevenueDailyRoutineResponse{}, err
	}
	return out, nil
}

func (c *Client) CreateRevenueChannelDraft(ctx context.Context, item RevenueChannelDraft) (RevenueChannelDraftResponse, error) {
	if err := validateRevenueChannelDraftRequest(item); err != nil {
		return RevenueChannelDraftResponse{}, err
	}
	var out RevenueChannelDraftResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/revenue/channel-drafts", item, &out); err != nil {
		return RevenueChannelDraftResponse{}, err
	}
	if err := validateRevenueChannelDraftResponse(out, item); err != nil {
		return RevenueChannelDraftResponse{}, err
	}
	return out, nil
}

func (c *Client) ApplyRevenueExternalSend(ctx context.Context, item RevenueExternalSendApplyRequest) (RevenueExternalSendApplyResponse, error) {
	if err := validateRevenueExternalSendApplyRequest(item); err != nil {
		return RevenueExternalSendApplyResponse{}, err
	}
	var out RevenueExternalSendApplyResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/revenue/channel-drafts/external-send-apply", item, &out); err != nil {
		return RevenueExternalSendApplyResponse{}, err
	}
	if err := validateRevenueExternalSendApplyResponse(out, item); err != nil {
		return RevenueExternalSendApplyResponse{}, err
	}
	return out, nil
}

func (c *Client) SkillGovernanceStatus(ctx context.Context, limit int) (SkillGovernanceStatus, error) {
	path := "/viewer/skill-governance/recent"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out SkillGovernanceStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SkillGovernanceStatus{}, err
	}
	if err := validateSkillGovernanceStatus(out); err != nil {
		return SkillGovernanceStatus{}, err
	}
	return out, nil
}

func (c *Client) SubmitSkillGovernanceExternalPR(ctx context.Context, item SkillGovernanceExternalPRSubmitRequest) (SkillGovernanceExternalPRSubmitResponse, error) {
	if err := validateSkillGovernanceExternalPRSubmitRequest(item); err != nil {
		return SkillGovernanceExternalPRSubmitResponse{}, err
	}
	var out SkillGovernanceExternalPRSubmitResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/skill-governance/external-pr-submit", item, &out); err != nil {
		return SkillGovernanceExternalPRSubmitResponse{}, err
	}
	if err := validateSkillGovernanceExternalPRSubmitResponse(out, item); err != nil {
		return SkillGovernanceExternalPRSubmitResponse{}, err
	}
	return out, nil
}

func validateRevenueStatus(resp RevenueStatus) error {
	if strings.TrimSpace(resp.ExternalChannelAdapter) == "" {
		return fmt.Errorf("revenue status missing external_channel_adapter")
	}
	if resp.ExternalChannelAdapterConfigured == nil {
		return fmt.Errorf("revenue status missing external_channel_adapter_configured")
	}
	if resp.HumanApprovalRequiredForExternalSend == nil {
		return fmt.Errorf("revenue status missing human_approval_required_for_external_send")
	}
	if strings.TrimSpace(resp.ExternalChannelAdapter) == "unconfigured" && *resp.ExternalChannelAdapterConfigured {
		return fmt.Errorf("revenue status unconfigured external channel adapter marked configured")
	}
	if !*resp.HumanApprovalRequiredForExternalSend {
		return fmt.Errorf("revenue status external send must require human approval")
	}
	if resp.Summary.PendingDecisionCount < 0 ||
		resp.Summary.DailyReportCount < 0 ||
		resp.Summary.ChannelDraftCount < 0 ||
		resp.Summary.ExternalSendApplyCount < 0 {
		return fmt.Errorf("revenue status summary counts must be >= 0")
	}
	decisions := map[string]struct{}{}
	for _, item := range resp.HumanDecisions {
		id := strings.TrimSpace(item.DecisionID)
		if id == "" {
			return fmt.Errorf("revenue status human_decisions missing decision_id")
		}
		if _, ok := decisions[id]; ok {
			return fmt.Errorf("revenue status duplicate human_decision decision_id=%s", id)
		}
		decisions[id] = struct{}{}
		if strings.TrimSpace(item.DecisionType) == "" {
			return fmt.Errorf("revenue status human_decision missing decision_type")
		}
		if strings.TrimSpace(item.ApprovalStatus) == "" {
			return fmt.Errorf("revenue status human_decision missing approval_status")
		}
		if strings.TrimSpace(item.GateStatus) == "" {
			return fmt.Errorf("revenue status human_decision missing gate_status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("revenue status human_decision %s missing created_at", id)
		}
	}
	reports := map[string]struct{}{}
	for _, item := range resp.DailyRoutineReports {
		id := strings.TrimSpace(item.ReportID)
		if id == "" {
			return fmt.Errorf("revenue status daily_routine_reports missing report_id")
		}
		if _, ok := reports[id]; ok {
			return fmt.Errorf("revenue status duplicate daily_routine_report report_id=%s", id)
		}
		reports[id] = struct{}{}
		if strings.TrimSpace(item.Date) == "" {
			return fmt.Errorf("revenue status daily_routine_report missing date")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("revenue status daily_routine_report missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("revenue status daily_routine_report %s missing created_at", id)
		}
		if item.ExternalSendApplied {
			return fmt.Errorf("revenue status daily_routine_report must not claim external send applied")
		}
	}
	drafts := map[string]struct{}{}
	for _, item := range resp.ChannelDrafts {
		id := strings.TrimSpace(item.DraftID)
		if id == "" {
			return fmt.Errorf("revenue status channel_drafts missing draft_id")
		}
		if _, ok := drafts[id]; ok {
			return fmt.Errorf("revenue status duplicate channel_draft draft_id=%s", id)
		}
		drafts[id] = struct{}{}
		if strings.TrimSpace(item.Channel) == "" {
			return fmt.Errorf("revenue status channel_draft missing channel")
		}
		if strings.TrimSpace(item.Body) == "" {
			return fmt.Errorf("revenue status channel_draft missing body")
		}
		if strings.TrimSpace(item.ApprovalStatus) == "" {
			return fmt.Errorf("revenue status channel_draft missing approval_status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("revenue status channel_draft %s missing created_at", id)
		}
		if item.ExternalSendApplied {
			return fmt.Errorf("revenue status channel_draft must not claim external send applied")
		}
	}
	applies := map[string]struct{}{}
	for _, item := range resp.ExternalSendApplyRecords {
		id := strings.TrimSpace(item.ApplyID)
		if id == "" {
			return fmt.Errorf("revenue status external_send_apply_records missing apply_id")
		}
		if _, ok := applies[id]; ok {
			return fmt.Errorf("revenue status duplicate external_send_apply_record apply_id=%s", id)
		}
		applies[id] = struct{}{}
		if strings.TrimSpace(item.DraftID) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing draft_id")
		}
		if strings.TrimSpace(item.DecisionID) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing decision_id")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("revenue status external_send_apply_record %s missing created_at", id)
		}
		if strings.TrimSpace(item.Channel) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing channel")
		}
		if strings.TrimSpace(item.ApprovalStatus) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing approval_status")
		}
		if strings.TrimSpace(item.ApprovalStatus) != "approved" {
			return fmt.Errorf("revenue status external_send_apply_record approval_status must be approved")
		}
		if !item.HumanApproved {
			return fmt.Errorf("revenue status external_send_apply_record missing human approval")
		}
		if strings.TrimSpace(item.ApplyStatus) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing apply_status")
		}
		switch strings.TrimSpace(item.ApplyStatus) {
		case "blocked", "failed", "sent":
		default:
			return fmt.Errorf("revenue status external_send_apply_record invalid apply_status=%q", item.ApplyStatus)
		}
		if strings.TrimSpace(item.SendResult) == "" {
			return fmt.Errorf("revenue status external_send_apply_record missing send_result")
		}
		if strings.TrimSpace(item.ApplyStatus) != "sent" && strings.TrimSpace(item.SendResult) == "sent" {
			return fmt.Errorf("revenue status external_send_apply_record send_result=sent requires sent apply_status")
		}
		if !*resp.ExternalChannelAdapterConfigured && recordAdapterConfigured(item.ChannelAdapter) {
			return fmt.Errorf("revenue status external_send_apply_record channel_adapter conflicts with unconfigured external channel adapter")
		}
		if item.ExternalSendApplied {
			if strings.TrimSpace(item.ApplyStatus) != "sent" {
				return fmt.Errorf("revenue status external_send_apply_record claims applied but apply_status=%q", item.ApplyStatus)
			}
			if !item.PostSendVerified || strings.TrimSpace(item.PostSendEvidence) == "" {
				return fmt.Errorf("revenue status external_send_apply_record claims applied without post-send verification evidence")
			}
		} else {
			if item.PostSendVerified || strings.TrimSpace(item.PostSendEvidence) != "" {
				return fmt.Errorf("revenue status external_send_apply_record claims post-send verification without applied send")
			}
			if strings.TrimSpace(item.ApplyStatus) == "sent" {
				return fmt.Errorf("revenue status external_send_apply_record claims sent without external_send_applied")
			}
			if strings.TrimSpace(item.FailureReason) == "" {
				return fmt.Errorf("revenue status external_send_apply_record missing failure_reason for unsent external send")
			}
		}
	}
	if resp.Summary.ExternalActionsApplied {
		for _, item := range resp.ExternalSendApplyRecords {
			if item.ExternalSendApplied && item.PostSendVerified && strings.TrimSpace(item.PostSendEvidence) != "" {
				return nil
			}
		}
		return fmt.Errorf("revenue status summary claims external actions applied without verified send record")
	}
	return nil
}

func validateSkillGovernanceStatus(resp SkillGovernanceStatus) error {
	if strings.TrimSpace(resp.ExternalPRAdapter) == "" {
		return fmt.Errorf("skill governance status missing external_pr_adapter")
	}
	if resp.ExternalPRAdapterConfigured == nil || resp.HumanApprovalRequiredForPR == nil {
		return fmt.Errorf("skill governance status missing external PR readiness fields")
	}
	if *resp.ExternalPRAdapterConfigured && strings.TrimSpace(resp.ExternalPRAdapter) == "unconfigured" {
		return fmt.Errorf("skill governance status external_pr_adapter_configured conflicts with unconfigured adapter")
	}
	if !*resp.HumanApprovalRequiredForPR {
		return fmt.Errorf("skill governance status external PR must require human approval")
	}
	manifests := map[string]struct{}{}
	for _, item := range resp.Manifests {
		id := strings.TrimSpace(item.SkillID)
		if id == "" {
			return fmt.Errorf("skill governance status manifests missing skill_id")
		}
		if _, ok := manifests[id]; ok {
			return fmt.Errorf("skill governance status duplicate manifest skill_id=%s", id)
		}
		manifests[id] = struct{}{}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("skill governance status manifest missing name")
		}
		if strings.TrimSpace(item.Scope) == "" {
			return fmt.Errorf("skill governance status manifest missing scope")
		}
		if strings.TrimSpace(item.Path) == "" {
			return fmt.Errorf("skill governance status manifest missing path")
		}
	}
	triggers := map[string]struct{}{}
	for _, item := range resp.TriggerLogs {
		id := strings.TrimSpace(item.EventID)
		if id == "" {
			return fmt.Errorf("skill governance status trigger_logs missing event_id")
		}
		if _, ok := triggers[id]; ok {
			return fmt.Errorf("skill governance status duplicate trigger_log event_id=%s", id)
		}
		triggers[id] = struct{}{}
		if strings.TrimSpace(item.SkillID) == "" {
			return fmt.Errorf("skill governance status trigger_log missing skill_id")
		}
		if strings.TrimSpace(item.TriggerType) == "" {
			return fmt.Errorf("skill governance status trigger_log missing trigger_type")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("skill governance status trigger_log missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("skill governance status trigger_log %s missing created_at", id)
		}
	}
	changes := map[string]struct{}{}
	for _, item := range resp.ChangeLogs {
		id := strings.TrimSpace(item.ChangeID)
		if id == "" {
			return fmt.Errorf("skill governance status change_logs missing change_id")
		}
		if _, ok := changes[id]; ok {
			return fmt.Errorf("skill governance status duplicate change_log change_id=%s", id)
		}
		changes[id] = struct{}{}
		if strings.TrimSpace(item.SkillID) == "" {
			return fmt.Errorf("skill governance status change_log missing skill_id")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("skill governance status change_log %s missing created_at", id)
		}
	}
	contributions := map[string]struct{}{}
	for _, item := range resp.Contributions {
		id := strings.TrimSpace(item.EventID)
		if id == "" {
			return fmt.Errorf("skill governance status contributions missing event_id")
		}
		if _, ok := contributions[id]; ok {
			return fmt.Errorf("skill governance status duplicate contribution event_id=%s", id)
		}
		contributions[id] = struct{}{}
		if strings.TrimSpace(item.Repo) == "" {
			return fmt.Errorf("skill governance status contribution missing repo")
		}
		if strings.TrimSpace(item.GateStatus) == "" {
			return fmt.Errorf("skill governance status contribution missing gate_status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("skill governance status contribution %s missing created_at", id)
		}
	}
	submits := map[string]struct{}{}
	for _, item := range resp.ExternalPRSubmitRecords {
		id := strings.TrimSpace(item.SubmitID)
		if id == "" {
			return fmt.Errorf("skill governance status external_pr_submit_records missing submit_id")
		}
		if _, ok := submits[id]; ok {
			return fmt.Errorf("skill governance status duplicate external_pr_submit_record submit_id=%s", id)
		}
		submits[id] = struct{}{}
		if strings.TrimSpace(item.ContributionEventID) == "" {
			return fmt.Errorf("skill governance status external_pr_submit_record missing contribution_event_id")
		}
		if strings.TrimSpace(item.Repo) == "" {
			return fmt.Errorf("skill governance status external_pr_submit_record missing repo")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("skill governance status external_pr_submit_record %s missing created_at", id)
		}
		if strings.TrimSpace(item.ApprovalStatus) == "" {
			return fmt.Errorf("skill governance status external_pr_submit_record missing approval_status")
		}
		if strings.TrimSpace(item.ApprovalStatus) != "approved" {
			return fmt.Errorf("skill governance status external_pr_submit_record approval_status must be approved")
		}
		if !item.HumanApproved {
			return fmt.Errorf("skill governance status external_pr_submit_record missing human approval")
		}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("skill governance status external_pr_submit_record missing title")
		}
		if strings.TrimSpace(item.SubmitStatus) == "" {
			return fmt.Errorf("skill governance status external_pr_submit_record missing submit_status")
		}
		switch strings.TrimSpace(item.SubmitStatus) {
		case "blocked", "failed", "created":
		default:
			return fmt.Errorf("skill governance status external_pr_submit_record invalid submit_status=%q", item.SubmitStatus)
		}
		if !*resp.ExternalPRAdapterConfigured && recordAdapterConfigured(item.PRAdapter) {
			return fmt.Errorf("skill governance status external_pr_submit_record pr_adapter conflicts with unconfigured external PR adapter")
		}
		if item.ExternalPRCreated {
			if strings.TrimSpace(item.SubmitStatus) != "created" {
				return fmt.Errorf("skill governance status external_pr_submit_record claims created but submit_status=%q", item.SubmitStatus)
			}
			if strings.TrimSpace(item.PRURL) == "" {
				return fmt.Errorf("skill governance status external_pr_submit_record claims created without pr_url")
			}
			if !item.PostSubmitVerified || strings.TrimSpace(item.PostSubmitEvidence) == "" {
				return fmt.Errorf("skill governance status external_pr_submit_record claims created without post-submit verification evidence")
			}
		} else {
			if strings.TrimSpace(item.SubmitStatus) == "created" {
				return fmt.Errorf("skill governance status external_pr_submit_record claims created without external_pr_created")
			}
			if strings.TrimSpace(item.PRURL) != "" {
				return fmt.Errorf("skill governance status external_pr_submit_record has pr_url without external_pr_created")
			}
			if item.PostSubmitVerified || strings.TrimSpace(item.PostSubmitEvidence) != "" {
				return fmt.Errorf("skill governance status external_pr_submit_record claims post-submit verification without external_pr_created")
			}
			if strings.TrimSpace(item.FailureReason) == "" {
				return fmt.Errorf("skill governance status external_pr_submit_record missing failure_reason for uncreated external PR")
			}
		}
	}
	transcripts := map[string]struct{}{}
	transcriptEvidence := map[string]map[string]bool{}
	for _, item := range resp.CoderTranscripts {
		id := strings.TrimSpace(item.EventID)
		if id == "" {
			return fmt.Errorf("skill governance status coder_transcripts missing event_id")
		}
		if _, ok := transcripts[id]; ok {
			return fmt.Errorf("skill governance status duplicate coder_transcript event_id=%s", id)
		}
		transcripts[id] = struct{}{}
		if strings.TrimSpace(item.Role) == "" {
			return fmt.Errorf("skill governance status coder_transcript missing role")
		}
		if strings.TrimSpace(item.Segment) == "" {
			return fmt.Errorf("skill governance status coder_transcript missing segment")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("skill governance status coder_transcript %s missing created_at", id)
		}
		evidencePath := strings.TrimSpace(item.EvidencePath)
		if evidencePath != "" {
			clean := filepath.Clean(evidencePath)
			if filepath.IsAbs(evidencePath) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
				return fmt.Errorf("skill governance status coder_transcript invalid evidence_path")
			}
		}
		switch strings.TrimSpace(item.Segment) {
		case "patch_evidence", "transcript_evidence":
			if evidencePath == "" {
				return fmt.Errorf("skill governance status coder_transcript %s missing evidence_path", item.Segment)
			}
			key := strings.TrimSpace(item.JobID)
			if key == "" {
				key = strings.TrimSpace(item.SessionID)
			}
			if key == "" {
				return fmt.Errorf("skill governance status coder_transcript evidence missing job_id or session_id")
			}
			if transcriptEvidence[key] == nil {
				transcriptEvidence[key] = map[string]bool{}
			}
			transcriptEvidence[key][strings.TrimSpace(item.Segment)] = true
		}
	}
	for key, seen := range transcriptEvidence {
		if !seen["patch_evidence"] || !seen["transcript_evidence"] {
			return fmt.Errorf("skill governance status coder_transcript incomplete evidence pair for job_id=%s", key)
		}
	}
	return nil
}

func validateRevenueDailyRoutineResponse(resp RevenueDailyRoutineResponse, req RevenueDailyRoutineRequest) error {
	report := resp.Report
	if strings.TrimSpace(req.ReportID) != "" && strings.TrimSpace(report.ReportID) != strings.TrimSpace(req.ReportID) {
		return fmt.Errorf("revenue daily routine response report_id mismatch")
	}
	if strings.TrimSpace(req.WorkstreamID) != "" && strings.TrimSpace(report.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
		return fmt.Errorf("revenue daily routine response workstream_id mismatch")
	}
	if strings.TrimSpace(req.Date) != "" && strings.TrimSpace(report.Date) != strings.TrimSpace(req.Date) {
		return fmt.Errorf("revenue daily routine response date mismatch")
	}
	if strings.TrimSpace(report.Status) != "draft_report" {
		return fmt.Errorf("revenue daily routine response status must be draft_report")
	}
	if resp.ExternalActionsApplied || report.ExternalSendApplied {
		return fmt.Errorf("revenue daily routine response applied external action")
	}
	if !resp.HumanApprovalRequiredForExternalActions {
		return fmt.Errorf("revenue daily routine response missing human approval requirement")
	}
	if report.CreatedAt.IsZero() {
		return fmt.Errorf("revenue daily routine response report missing created_at")
	}
	return nil
}

func validateRevenueHumanDecisionRequest(item RevenueHumanDecision) error {
	if strings.TrimSpace(item.DecisionType) == "" {
		return fmt.Errorf("revenue human decision request missing decision_type")
	}
	return nil
}

func validateRevenueHumanDecisionReviewRequest(item RevenueHumanDecisionReview) error {
	if strings.TrimSpace(item.DecisionID) == "" {
		return fmt.Errorf("revenue human decision review request missing decision_id")
	}
	if strings.TrimSpace(item.ApprovalStatus) == "" {
		return fmt.Errorf("revenue human decision review request missing approval_status")
	}
	return nil
}

func validateRevenueDailyRoutineRequest(item RevenueDailyRoutineRequest) error {
	if item.Limit < 0 {
		return fmt.Errorf("revenue daily routine request limit must be >= 0")
	}
	return nil
}

func validateRevenueChannelDraftRequest(item RevenueChannelDraft) error {
	if strings.TrimSpace(item.DraftID) == "" {
		return fmt.Errorf("revenue channel draft request missing draft_id")
	}
	if strings.TrimSpace(item.Channel) == "" {
		return fmt.Errorf("revenue channel draft request missing channel")
	}
	if strings.TrimSpace(item.Body) == "" {
		return fmt.Errorf("revenue channel draft request missing body")
	}
	return nil
}

func validateRevenueExternalSendApplyRequest(item RevenueExternalSendApplyRequest) error {
	if strings.TrimSpace(item.ApplyID) == "" {
		return fmt.Errorf("revenue external send apply request missing apply_id")
	}
	if strings.TrimSpace(item.DraftID) == "" {
		return fmt.Errorf("revenue external send apply request missing draft_id")
	}
	if strings.TrimSpace(item.DecisionID) == "" {
		return fmt.Errorf("revenue external send apply request missing decision_id")
	}
	if !item.HumanApproved {
		return fmt.Errorf("revenue external send apply request requires human_approved")
	}
	return nil
}

func validateComplexityConcreteDiffRequest(req ComplexityConcreteDiffRequest) error {
	if strings.TrimSpace(req.HotspotID) == "" {
		return fmt.Errorf("complexity concrete diff request missing hotspot_id")
	}
	if strings.TrimSpace(req.ConcreteDiff) == "" {
		return fmt.Errorf("complexity concrete diff request missing concrete_diff")
	}
	return nil
}

func validateComplexityCoderDiffRequest(req ComplexityCoderDiffRequest) error {
	if strings.TrimSpace(req.HotspotID) == "" {
		return fmt.Errorf("complexity coder diff request missing hotspot_id")
	}
	return nil
}

func validateComplexityDiffResponse(resp ComplexityDiffResponse, req ComplexityConcreteDiffRequest, coderReq ComplexityCoderDiffRequest, requireCoderResult bool) error {
	if strings.TrimSpace(req.HotspotID) != "" && strings.TrimSpace(resp.Hotspot.HotspotID) != strings.TrimSpace(req.HotspotID) {
		return fmt.Errorf("complexity diff response hotspot_id mismatch")
	}
	if resp.Hotspot.CreatedAt.IsZero() {
		return fmt.Errorf("complexity diff response hotspot missing created_at")
	}
	if !resp.HumanApprovalRequired {
		return fmt.Errorf("complexity diff response missing human approval requirement")
	}
	if resp.PatchApplied {
		return fmt.Errorf("complexity diff response must not claim patch_applied")
	}
	artifact := resp.ConcreteDiffArtifact
	if strings.TrimSpace(artifact.ArtifactID) == "" {
		return fmt.Errorf("complexity diff response missing concrete diff artifact")
	}
	if strings.TrimSpace(req.ArtifactID) != "" && strings.TrimSpace(artifact.ArtifactID) != strings.TrimSpace(req.ArtifactID) {
		return fmt.Errorf("complexity diff response artifact_id mismatch")
	}
	if strings.TrimSpace(artifact.Type) != "complexity_concrete_diff_proposal" {
		return fmt.Errorf("complexity diff response artifact type mismatch")
	}
	if strings.TrimSpace(artifact.Status) != "pending_review" {
		return fmt.Errorf("complexity diff response artifact status must be pending_review")
	}
	if strings.TrimSpace(resp.Hotspot.ScanID) != "" && strings.TrimSpace(artifact.ScanID) != strings.TrimSpace(resp.Hotspot.ScanID) {
		return fmt.Errorf("complexity diff response artifact scan_id mismatch")
	}
	if artifact.CreatedAt.IsZero() {
		return fmt.Errorf("complexity diff response artifact missing created_at")
	}
	if resp.WorkstreamArtifact != nil {
		if strings.TrimSpace(req.WorkstreamID) != "" && strings.TrimSpace(resp.WorkstreamArtifact.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
			return fmt.Errorf("complexity diff response workstream_id mismatch")
		}
		if strings.TrimSpace(resp.WorkstreamArtifact.Type) != "complexity_concrete_diff_review" {
			return fmt.Errorf("complexity diff response workstream artifact type mismatch")
		}
		if strings.TrimSpace(resp.WorkstreamArtifact.Status) != "pending_review" {
			return fmt.Errorf("complexity diff response workstream artifact status must be pending_review")
		}
		if resp.WorkstreamArtifact.CreatedAt.IsZero() {
			return fmt.Errorf("complexity diff response workstream artifact missing created_at")
		}
	}
	if strings.TrimSpace(req.SandboxID) != "" {
		if resp.SandboxPromotion == nil || resp.SandboxDecision == nil || resp.SandboxGateLog == nil {
			return fmt.Errorf("complexity diff response missing sandbox promotion payload")
		}
		if resp.SandboxPromotion.CreatedAt.IsZero() {
			return fmt.Errorf("complexity diff response sandbox promotion missing created_at")
		}
		if resp.SandboxGateLog.CreatedAt.IsZero() {
			return fmt.Errorf("complexity diff response sandbox gate log missing created_at")
		}
		if strings.TrimSpace(resp.SandboxPromotion.SandboxID) != strings.TrimSpace(req.SandboxID) {
			return fmt.Errorf("complexity diff response sandbox_id mismatch")
		}
		if strings.TrimSpace(req.PromotionID) != "" && strings.TrimSpace(resp.SandboxPromotion.PromotionID) != strings.TrimSpace(req.PromotionID) {
			return fmt.Errorf("complexity diff response promotion_id mismatch")
		}
		if strings.TrimSpace(req.TargetPath) != "" && strings.TrimSpace(resp.SandboxPromotion.TargetPath) != strings.TrimSpace(req.TargetPath) {
			return fmt.Errorf("complexity diff response target_path mismatch")
		}
		if strings.TrimSpace(req.DiffPath) != "" && strings.TrimSpace(resp.SandboxPromotion.DiffPath) != strings.TrimSpace(req.DiffPath) {
			return fmt.Errorf("complexity diff response diff_path mismatch")
		}
		if strings.TrimSpace(resp.SandboxGateLog.PromotionID) != strings.TrimSpace(resp.SandboxPromotion.PromotionID) {
			return fmt.Errorf("complexity diff response sandbox gate promotion_id mismatch")
		}
		if strings.TrimSpace(resp.SandboxGateLog.GateStatus) != strings.TrimSpace(resp.SandboxDecision.Status) {
			return fmt.Errorf("complexity diff response sandbox gate status mismatch")
		}
		switch strings.TrimSpace(resp.SandboxDecision.Status) {
		case "approve", "reject", "needs_review", "needs_more_tests":
		default:
			return fmt.Errorf("complexity diff response invalid sandbox decision status")
		}
	}
	if requireCoderResult {
		if resp.CoderResult == nil {
			return fmt.Errorf("complexity coder diff response missing coder_result")
		}
		if strings.TrimSpace(coderReq.JobID) != "" && strings.TrimSpace(resp.CoderResult.JobID) != strings.TrimSpace(coderReq.JobID) {
			return fmt.Errorf("complexity coder diff response job_id mismatch")
		}
		if strings.TrimSpace(resp.CoderResult.ConcreteDiff) == "" {
			return fmt.Errorf("complexity coder diff response missing concrete_diff")
		}
	}
	return nil
}

func validateRevenueChannelDraftResponse(resp RevenueChannelDraftResponse, req RevenueChannelDraft) error {
	if strings.TrimSpace(req.DraftID) != "" && strings.TrimSpace(resp.Draft.DraftID) != strings.TrimSpace(req.DraftID) {
		return fmt.Errorf("revenue channel draft response draft_id mismatch")
	}
	if strings.TrimSpace(req.WorkstreamID) != "" && strings.TrimSpace(resp.Draft.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
		return fmt.Errorf("revenue channel draft response workstream_id mismatch")
	}
	if strings.TrimSpace(req.Channel) != "" && strings.TrimSpace(resp.Draft.Channel) != strings.TrimSpace(req.Channel) {
		return fmt.Errorf("revenue channel draft response channel mismatch")
	}
	if resp.ExternalActionsApplied {
		return fmt.Errorf("revenue channel draft response applied external action")
	}
	if resp.Draft.ExternalSendApplied {
		return fmt.Errorf("revenue channel draft response draft claims external send applied")
	}
	if !resp.HumanApprovalRequiredForExternalSendApply {
		return fmt.Errorf("revenue channel draft response missing human approval requirement")
	}
	approvalStatus := strings.TrimSpace(resp.Draft.ApprovalStatus)
	if approvalStatus != "" && approvalStatus != "pending" {
		return fmt.Errorf("revenue channel draft response approval_status must be pending")
	}
	if resp.Draft.CreatedAt.IsZero() {
		return fmt.Errorf("revenue channel draft response draft missing created_at")
	}
	return nil
}

func validateRevenueExternalSendApplyResponse(resp RevenueExternalSendApplyResponse, req RevenueExternalSendApplyRequest) error {
	record := resp.Record
	if strings.TrimSpace(record.ApplyID) != strings.TrimSpace(req.ApplyID) {
		return fmt.Errorf("external send apply response apply_id mismatch")
	}
	if strings.TrimSpace(record.DraftID) != strings.TrimSpace(req.DraftID) {
		return fmt.Errorf("external send apply response draft_id mismatch")
	}
	if strings.TrimSpace(record.DecisionID) != strings.TrimSpace(req.DecisionID) {
		return fmt.Errorf("external send apply response decision_id mismatch")
	}
	if resp.ExternalActionsApplied != record.ExternalSendApplied {
		return fmt.Errorf("external send apply response mismatch: external_actions_applied=%t record.external_send_applied=%t", resp.ExternalActionsApplied, record.ExternalSendApplied)
	}
	if resp.PostSendVerified != record.PostSendVerified {
		return fmt.Errorf("external send apply response mismatch: post_send_verified=%t record.post_send_verified=%t", resp.PostSendVerified, record.PostSendVerified)
	}
	if !record.HumanApproved {
		return fmt.Errorf("external send apply response record missing human approval")
	}
	if strings.TrimSpace(record.ApprovalStatus) != "approved" {
		return fmt.Errorf("external send apply response approval_status must be approved")
	}
	if strings.TrimSpace(record.ApplyStatus) != "sent" && strings.TrimSpace(record.SendResult) == "sent" {
		return fmt.Errorf("external send apply response send_result=sent requires sent apply_status")
	}
	if !resp.ExternalActionsApplied && strings.TrimSpace(resp.ExternalChannelAdapterConfiguration) == "required" && recordAdapterConfigured(record.ChannelAdapter) {
		return fmt.Errorf("external send apply response channel_adapter conflicts with required external channel adapter configuration")
	}
	if !resp.ExternalActionsApplied {
		if strings.TrimSpace(record.ApplyStatus) == "sent" || record.ExternalSendApplied || record.PostSendVerified {
			return fmt.Errorf("external send apply response is not applied but record claims sent state")
		}
		if strings.TrimSpace(record.FailureReason) == "" {
			return fmt.Errorf("external send apply response is not applied without failure_reason")
		}
		if !resp.HumanApprovalRequiredForRetry {
			return fmt.Errorf("external send apply response missing human approval requirement for retry")
		}
		if record.CreatedAt.IsZero() {
			return fmt.Errorf("external send apply response record missing created_at")
		}
		return nil
	}
	if strings.TrimSpace(record.ApplyStatus) != "sent" {
		return fmt.Errorf("external send apply response claims applied but apply_status=%q", record.ApplyStatus)
	}
	if !record.PostSendVerified || strings.TrimSpace(record.PostSendEvidence) == "" {
		return fmt.Errorf("external send apply response claims applied without post-send verification evidence")
	}
	if record.CreatedAt.IsZero() {
		return fmt.Errorf("external send apply response record missing created_at")
	}
	return nil
}

func validateSkillGovernanceExternalPRSubmitResponse(resp SkillGovernanceExternalPRSubmitResponse, req SkillGovernanceExternalPRSubmitRequest) error {
	record := resp.Record
	if strings.TrimSpace(record.SubmitID) != strings.TrimSpace(req.SubmitID) {
		return fmt.Errorf("external PR submit response submit_id mismatch")
	}
	if strings.TrimSpace(record.ContributionEventID) != strings.TrimSpace(req.ContributionEventID) {
		return fmt.Errorf("external PR submit response contribution_event_id mismatch")
	}
	if strings.TrimSpace(record.Repo) != strings.TrimSpace(req.Repo) {
		return fmt.Errorf("external PR submit response repo mismatch")
	}
	if strings.TrimSpace(record.Title) != strings.TrimSpace(req.Title) {
		return fmt.Errorf("external PR submit response title mismatch")
	}
	if resp.ExternalPRCreated != record.ExternalPRCreated {
		return fmt.Errorf("external PR submit response mismatch: external_pr_created=%t record.external_pr_created=%t", resp.ExternalPRCreated, record.ExternalPRCreated)
	}
	if resp.PostSubmitVerified != record.PostSubmitVerified {
		return fmt.Errorf("external PR submit response mismatch: post_submit_verified=%t record.post_submit_verified=%t", resp.PostSubmitVerified, record.PostSubmitVerified)
	}
	if !resp.HumanApprovalRequiredForPR {
		return fmt.Errorf("external PR submit response missing human approval requirement")
	}
	if !record.HumanApproved {
		return fmt.Errorf("external PR submit response record missing human approval")
	}
	if strings.TrimSpace(record.ApprovalStatus) != "approved" {
		return fmt.Errorf("external PR submit response approval_status must be approved")
	}
	if !resp.ExternalPRCreated && strings.TrimSpace(resp.ExternalPRAdapterConfiguration) == "required" && recordAdapterConfigured(record.PRAdapter) {
		return fmt.Errorf("external PR submit response pr_adapter conflicts with required external PR adapter configuration")
	}
	if !resp.ExternalPRCreated {
		if strings.TrimSpace(record.SubmitStatus) == "created" || record.ExternalPRCreated || record.PostSubmitVerified || strings.TrimSpace(record.PRURL) != "" {
			return fmt.Errorf("external PR submit response is not created but record claims created state")
		}
		if strings.TrimSpace(record.FailureReason) == "" {
			return fmt.Errorf("external PR submit response is not created without failure_reason")
		}
		if record.CreatedAt.IsZero() {
			return fmt.Errorf("external PR submit response record missing created_at")
		}
		return nil
	}
	if strings.TrimSpace(record.SubmitStatus) != "created" {
		return fmt.Errorf("external PR submit response claims created but submit_status=%q", record.SubmitStatus)
	}
	if strings.TrimSpace(record.PRURL) == "" {
		return fmt.Errorf("external PR submit response claims created without pr_url")
	}
	if !record.PostSubmitVerified || strings.TrimSpace(record.PostSubmitEvidence) == "" {
		return fmt.Errorf("external PR submit response claims created without post-submit verification evidence")
	}
	if record.CreatedAt.IsZero() {
		return fmt.Errorf("external PR submit response record missing created_at")
	}
	return nil
}

func recordAdapterConfigured(adapter string) bool {
	trimmed := strings.TrimSpace(adapter)
	return trimmed != "" && trimmed != "unconfigured"
}

func validateSkillGovernanceExternalPRSubmitRequest(item SkillGovernanceExternalPRSubmitRequest) error {
	if strings.TrimSpace(item.SubmitID) == "" {
		return fmt.Errorf("external PR submit request missing submit_id")
	}
	if strings.TrimSpace(item.ContributionEventID) == "" {
		return fmt.Errorf("external PR submit request missing contribution_event_id")
	}
	if strings.TrimSpace(item.Repo) == "" {
		return fmt.Errorf("external PR submit request missing repo")
	}
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("external PR submit request missing title")
	}
	if !item.HumanApproved {
		return fmt.Errorf("external PR submit request requires human_approved")
	}
	return nil
}

func validateSkillGovernanceContributionGateRequest(item SkillGovernanceContributionGateRequest) error {
	if strings.TrimSpace(item.Repo) == "" {
		return fmt.Errorf("skill governance contribution gate request missing repo")
	}
	return nil
}

func validateRunQueueCompleteResponse(resp RunQueueCompleteResponse, req RunQueueCompleteRequest) error {
	if !resp.Completed {
		return fmt.Errorf("run queue complete response did not confirm completion")
	}
	if resp.Item.QueueID != strings.TrimSpace(req.QueueID) {
		return fmt.Errorf("run queue complete response queue_id mismatch")
	}
	expectedStatus := strings.TrimSpace(req.Status)
	if resp.Item.Status != expectedStatus {
		return fmt.Errorf("run queue complete response status mismatch: got %q want %q", resp.Item.Status, expectedStatus)
	}
	if resp.Item.CreatedAt.IsZero() {
		return fmt.Errorf("run queue complete response item missing created_at")
	}
	if isRunQueueTerminalStatus(expectedStatus) && resp.Item.CompletedAt.IsZero() {
		return fmt.Errorf("run queue complete response item missing completed_at")
	}
	return nil
}

func validateSuperAgentStatus(resp SuperAgentStatus) error {
	if err := validateSuperAgentRuntimeConfig(resp.RuntimeConfig); err != nil {
		return err
	}
	seenRuns := map[string]struct{}{}
	for _, run := range resp.AgentRuns {
		runID := strings.TrimSpace(run.RunID)
		if runID == "" {
			return fmt.Errorf("superagent status agent_run missing run_id")
		}
		status := strings.TrimSpace(run.Status)
		if status == "" {
			return fmt.Errorf("superagent status agent_run missing status")
		}
		if !isSuperAgentRunStatus(status) {
			return fmt.Errorf("superagent status invalid agent_run status=%q", run.Status)
		}
		if run.StartedAt.IsZero() {
			return fmt.Errorf("superagent status agent_run %q missing started_at", runID)
		}
		if isSuperAgentRunTerminalStatus(status) && run.CompletedAt.IsZero() {
			return fmt.Errorf("superagent status terminal agent_run %q missing completed_at", runID)
		}
		if status == "failed" && strings.TrimSpace(run.Summary) == "" {
			return fmt.Errorf("superagent status failed agent_run %q missing summary", runID)
		}
		if _, ok := seenRuns[runID]; ok {
			return fmt.Errorf("superagent status contains duplicate agent_run for run_id %q", runID)
		}
		seenRuns[runID] = struct{}{}
	}
	seenTasks := map[string]struct{}{}
	for _, task := range resp.SubagentTasks {
		subagentID := strings.TrimSpace(task.SubagentID)
		if subagentID == "" {
			return fmt.Errorf("superagent status subagent_task missing subagent_id")
		}
		if strings.TrimSpace(task.ParentRunID) == "" {
			return fmt.Errorf("superagent status subagent_task %q missing parent_run_id", subagentID)
		}
		if strings.TrimSpace(task.AgentType) == "" {
			return fmt.Errorf("superagent status subagent_task %q missing agent_type", subagentID)
		}
		if strings.TrimSpace(task.Task) == "" {
			return fmt.Errorf("superagent status subagent_task %q missing task", subagentID)
		}
		if len(task.Scope) == 0 {
			return fmt.Errorf("superagent status subagent_task %q missing scope", subagentID)
		}
		if strings.TrimSpace(task.TerminationCondition) == "" {
			return fmt.Errorf("superagent status subagent_task %q missing termination_condition", subagentID)
		}
		if strings.TrimSpace(task.Status) == "" {
			return fmt.Errorf("superagent status subagent_task %q missing status", subagentID)
		}
		if task.CreatedAt.IsZero() {
			return fmt.Errorf("superagent status subagent_task %q missing created_at", subagentID)
		}
		if _, ok := seenTasks[subagentID]; ok {
			return fmt.Errorf("superagent status contains duplicate subagent_task for subagent_id %q", subagentID)
		}
		seenTasks[subagentID] = struct{}{}
	}
	seenContexts := map[string]struct{}{}
	for _, pack := range resp.ContextPacks {
		contextPackID := strings.TrimSpace(pack.ContextPackID)
		if contextPackID == "" {
			return fmt.Errorf("superagent status context_pack missing context_pack_id")
		}
		if strings.TrimSpace(pack.RunID) == "" {
			return fmt.Errorf("superagent status context_pack %q missing run_id", contextPackID)
		}
		if strings.TrimSpace(pack.Summary) == "" {
			return fmt.Errorf("superagent status context_pack %q missing summary", contextPackID)
		}
		if pack.TokenEstimate < 0 {
			return fmt.Errorf("superagent status context_pack %q token_estimate must be >= 0", contextPackID)
		}
		if pack.CreatedAt.IsZero() {
			return fmt.Errorf("superagent status context_pack %q missing created_at", contextPackID)
		}
		if _, ok := seenContexts[contextPackID]; ok {
			return fmt.Errorf("superagent status contains duplicate context_pack for context_pack_id %q", contextPackID)
		}
		seenContexts[contextPackID] = struct{}{}
	}
	seenChannels := map[string]struct{}{}
	for _, channel := range resp.MessageChannels {
		channelID := strings.TrimSpace(channel.ChannelID)
		if channelID == "" {
			return fmt.Errorf("superagent status message_channel missing channel_id")
		}
		if strings.TrimSpace(channel.ChannelType) == "" {
			return fmt.Errorf("superagent status message_channel %q missing channel_type", channelID)
		}
		if strings.TrimSpace(channel.Status) == "" {
			return fmt.Errorf("superagent status message_channel %q missing status", channelID)
		}
		if channel.CreatedAt.IsZero() {
			return fmt.Errorf("superagent status message_channel %q missing created_at", channelID)
		}
		if _, ok := seenChannels[channelID]; ok {
			return fmt.Errorf("superagent status contains duplicate message_channel for channel_id %q", channelID)
		}
		seenChannels[channelID] = struct{}{}
	}
	seenEvents := map[string]struct{}{}
	for _, event := range resp.TraceEvents {
		eventID := strings.TrimSpace(event.EventID)
		if eventID == "" {
			return fmt.Errorf("superagent status trace_event missing event_id")
		}
		if strings.TrimSpace(event.EventType) == "" {
			return fmt.Errorf("superagent status trace_event %q missing event_type", eventID)
		}
		if strings.TrimSpace(event.Status) == "" {
			return fmt.Errorf("superagent status trace_event %q missing status", eventID)
		}
		if event.CreatedAt.IsZero() {
			return fmt.Errorf("superagent status trace_event %q missing created_at", eventID)
		}
		if _, ok := seenEvents[eventID]; ok {
			return fmt.Errorf("superagent status contains duplicate trace_event for event_id %q", eventID)
		}
		seenEvents[eventID] = struct{}{}
	}
	seenQueues := map[string]struct{}{}
	for _, item := range resp.RunQueue {
		queueID := strings.TrimSpace(item.QueueID)
		if queueID == "" {
			return fmt.Errorf("superagent status run_queue item missing queue_id")
		}
		status := strings.TrimSpace(item.Status)
		if status == "" {
			return fmt.Errorf("superagent status run_queue item missing status")
		}
		if !isRunQueueStatus(status) {
			return fmt.Errorf("superagent status invalid run_queue status=%q", item.Status)
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("superagent status run_queue item %q missing created_at", queueID)
		}
		if isRunQueueTerminalStatus(status) && item.CompletedAt.IsZero() {
			return fmt.Errorf("superagent status terminal run_queue item %q missing completed_at", queueID)
		}
		if status == "failed" && strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("superagent status failed run_queue item %q missing reason", queueID)
		}
		if _, ok := seenQueues[queueID]; ok {
			return fmt.Errorf("superagent status contains duplicate run_queue item for queue_id %q", queueID)
		}
		seenQueues[queueID] = struct{}{}
	}
	return nil
}

func validateSuperAgentRuntimeConfig(cfg SuperAgentRuntimeConfig) error {
	if !cfg.RunQueueSchedulerEnabled {
		return nil
	}
	if cfg.RunQueueSchedulerIntervalSec <= 0 {
		return fmt.Errorf("superagent status run_queue scheduler enabled with invalid interval_sec %d", cfg.RunQueueSchedulerIntervalSec)
	}
	if cfg.RunQueueSchedulerClaimLimit <= 0 {
		return fmt.Errorf("superagent status run_queue scheduler enabled with invalid claim_limit %d", cfg.RunQueueSchedulerClaimLimit)
	}
	return nil
}

func isSuperAgentRunTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "paused":
		return true
	default:
		return false
	}
}

func isSuperAgentRunStatus(status string) bool {
	switch status {
	case "running", "paused", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isRunQueueTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isRunQueueStatus(status string) bool {
	switch status {
	case "queued", "claimed", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func validateAIWorkflowStatus(resp AIWorkflowStatus) error {
	if err := validateContextBudgetPolicy(resp.ContextBudgetPolicy); err != nil {
		return err
	}
	seenEvents := map[string]struct{}{}
	for _, event := range resp.WorkflowEvents {
		eventID := strings.TrimSpace(event.EventID)
		if eventID == "" {
			return fmt.Errorf("ai workflow status workflow_event missing event_id")
		}
		if strings.TrimSpace(event.EventType) == "" {
			return fmt.Errorf("ai workflow status workflow_event missing event_type")
		}
		if strings.TrimSpace(event.Status) == "" {
			return fmt.Errorf("ai workflow status workflow_event missing status")
		}
		if event.CreatedAt.IsZero() {
			return fmt.Errorf("ai workflow status workflow_event missing created_at")
		}
		if _, ok := seenEvents[eventID]; ok {
			return fmt.Errorf("ai workflow status contains duplicate workflow_event for event_id %q", eventID)
		}
		seenEvents[eventID] = struct{}{}
	}
	seenMemory := map[string]struct{}{}
	for _, item := range resp.ProjectMemoryIndexes {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return fmt.Errorf("ai workflow status project_memory_index missing id")
		}
		if strings.TrimSpace(item.Repo) == "" {
			return fmt.Errorf("ai workflow status project_memory_index missing repo")
		}
		if strings.TrimSpace(item.FilePath) == "" {
			return fmt.Errorf("ai workflow status project_memory_index missing file_path")
		}
		if strings.TrimSpace(item.MemoryType) == "" {
			return fmt.Errorf("ai workflow status project_memory_index missing memory_type")
		}
		if item.UpdatedAt.IsZero() {
			return fmt.Errorf("ai workflow status project_memory_index missing updated_at")
		}
		if _, ok := seenMemory[id]; ok {
			return fmt.Errorf("ai workflow status contains duplicate project_memory_index for id %q", id)
		}
		seenMemory[id] = struct{}{}
	}
	seenWorktrees := map[string]struct{}{}
	for _, item := range resp.WorktreeRegistries {
		worktreeID := strings.TrimSpace(item.WorktreeID)
		if worktreeID == "" {
			return fmt.Errorf("ai workflow status worktree_registry missing worktree_id")
		}
		if strings.TrimSpace(item.Repo) == "" {
			return fmt.Errorf("ai workflow status worktree_registry missing repo")
		}
		if strings.TrimSpace(item.Path) == "" {
			return fmt.Errorf("ai workflow status worktree_registry missing path")
		}
		if strings.TrimSpace(item.Branch) == "" {
			return fmt.Errorf("ai workflow status worktree_registry missing branch")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("ai workflow status worktree_registry missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("ai workflow status worktree_registry missing created_at")
		}
		if _, ok := seenWorktrees[worktreeID]; ok {
			return fmt.Errorf("ai workflow status contains duplicate worktree_registry for worktree_id %q", worktreeID)
		}
		seenWorktrees[worktreeID] = struct{}{}
	}
	seenCommands := map[string]struct{}{}
	for _, item := range resp.CommandRegistries {
		commandName := strings.TrimSpace(item.CommandName)
		if commandName == "" {
			return fmt.Errorf("ai workflow status command_registry missing command_name")
		}
		if strings.TrimSpace(item.FilePath) == "" {
			return fmt.Errorf("ai workflow status command_registry missing file_path")
		}
		if item.UpdatedAt.IsZero() {
			return fmt.Errorf("ai workflow status command_registry missing updated_at")
		}
		if _, ok := seenCommands[commandName]; ok {
			return fmt.Errorf("ai workflow status contains duplicate command_registry for command_name %q", commandName)
		}
		seenCommands[commandName] = struct{}{}
	}
	seenUsages := map[string]struct{}{}
	for _, usage := range resp.ContextUsages {
		eventID := strings.TrimSpace(usage.EventID)
		if eventID == "" {
			return fmt.Errorf("ai workflow status context_usage missing event_id")
		}
		if strings.TrimSpace(usage.Agent) == "" {
			return fmt.Errorf("ai workflow status context_usage missing agent")
		}
		if usage.CreatedAt.IsZero() {
			return fmt.Errorf("ai workflow status context_usage missing created_at")
		}
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ContextTokens < 0 ||
			usage.ToolCallCount < 0 || usage.DCICallCount < 0 || usage.RepairCount < 0 || usage.LatencyMS < 0 {
			return fmt.Errorf("ai workflow status context_usage counts must be >= 0")
		}
		if usage.EstimatedCost < 0 || usage.KVCacheEstimate < 0 {
			return fmt.Errorf("ai workflow status context_usage numeric estimates must be >= 0")
		}
		if _, ok := seenUsages[eventID]; ok {
			return fmt.Errorf("ai workflow status contains duplicate context_usage for event_id %q", eventID)
		}
		seenUsages[eventID] = struct{}{}
	}
	return nil
}

func validateToolHarnessStatus(resp ToolHarnessStatus) error {
	seen := map[string]struct{}{}
	for _, item := range resp.Items {
		eventID := strings.TrimSpace(item.EventID)
		if eventID == "" {
			return fmt.Errorf("tool harness status event missing event_id")
		}
		if _, ok := seen[eventID]; ok {
			return fmt.Errorf("tool harness status contains duplicate event for event_id %q", eventID)
		}
		seen[eventID] = struct{}{}
		if strings.TrimSpace(item.ToolName) == "" {
			return fmt.Errorf("tool harness status event missing tool_name")
		}
		if strings.TrimSpace(item.RawInputHash) == "" {
			return fmt.Errorf("tool harness status event missing raw_input_hash")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("tool harness status event missing created_at")
		}
		status := strings.TrimSpace(item.ValidationStatus)
		switch status {
		case "valid", "repaired":
		default:
			return fmt.Errorf("tool harness status event has invalid validation_status %q", item.ValidationStatus)
		}
		hasRepairEvidence := len(item.Repairs) > 0 || len(item.RelationDefaults) > 0
		if status == "valid" && hasRepairEvidence {
			return fmt.Errorf("tool harness status valid event includes repair evidence")
		}
		if status == "repaired" && !hasRepairEvidence {
			return fmt.Errorf("tool harness status repaired event missing repair evidence")
		}
		for _, repair := range item.Repairs {
			if strings.TrimSpace(repair.Type) == "" {
				return fmt.Errorf("tool harness status repair missing type")
			}
		}
		for _, def := range item.RelationDefaults {
			if strings.TrimSpace(def.Field) == "" {
				return fmt.Errorf("tool harness status relation default missing field")
			}
		}
	}
	return nil
}

func validateDCIRecentStatus(resp DCIRecentStatus) error {
	seen := map[string]struct{}{}
	for _, item := range resp.Items {
		if err := validateDCISearchTrace(item, "dci recent"); err != nil {
			return err
		}
		eventID := strings.TrimSpace(item.EventID)
		if _, ok := seen[eventID]; ok {
			return fmt.Errorf("dci recent contains duplicate trace for event_id %q", eventID)
		}
		seen[eventID] = struct{}{}
	}
	return nil
}

func validateDCISearchRequest(req DCISearchRequest) error {
	if strings.TrimSpace(req.Query) == "" {
		return fmt.Errorf("dci search request missing query")
	}
	return nil
}

func validateDCISearchResult(resp DCISearchResult, req DCISearchRequest) error {
	if strings.TrimSpace(resp.Pack.EventID) == "" {
		return fmt.Errorf("dci search response pack missing event_id")
	}
	if strings.TrimSpace(resp.Trace.EventID) == "" {
		return fmt.Errorf("dci search response trace missing event_id")
	}
	if resp.Pack.EventID != resp.Trace.EventID {
		return fmt.Errorf("dci search response event_id mismatch")
	}
	if strings.TrimSpace(resp.Pack.Query) != strings.TrimSpace(req.Query) {
		return fmt.Errorf("dci search response query mismatch")
	}
	if resp.Pack.Confidence < 0 || resp.Pack.Confidence > 1 {
		return fmt.Errorf("dci search response pack confidence out of range")
	}
	if err := validateDCISearchTrace(resp.Trace, "dci search response"); err != nil {
		return err
	}
	if resp.Trace.FinalEvidenceCount != len(resp.Pack.Evidence) {
		return fmt.Errorf("dci search response evidence count mismatch")
	}
	for _, evidence := range resp.Pack.Evidence {
		if err := validateDCIEvidence(evidence); err != nil {
			return err
		}
	}
	return nil
}

func validateDCISearchTrace(trace DCISearchTrace, label string) error {
	if strings.TrimSpace(trace.EventID) == "" {
		return fmt.Errorf("%s trace missing event_id", label)
	}
	if trace.StartedAt.IsZero() {
		return fmt.Errorf("%s trace missing started_at", label)
	}
	if strings.TrimSpace(trace.Actor) == "" {
		return fmt.Errorf("%s trace missing actor", label)
	}
	if strings.TrimSpace(trace.Mode) == "" {
		return fmt.Errorf("%s trace missing mode", label)
	}
	if strings.TrimSpace(trace.Status) == "" {
		return fmt.Errorf("%s trace missing status", label)
	}
	status := strings.TrimSpace(trace.Status)
	if !isDCISearchTraceStatus(status) {
		return fmt.Errorf("%s invalid trace status %q", label, trace.Status)
	}
	if isDCISearchTerminalStatus(status) && trace.EndedAt.IsZero() {
		return fmt.Errorf("%s terminal trace %s missing ended_at", label, strings.TrimSpace(trace.EventID))
	}
	if status == "failed" && strings.TrimSpace(trace.ErrorMessage) == "" {
		return fmt.Errorf("%s failed trace missing error_message", label)
	}
	if strings.TrimSpace(trace.UserQuery) == "" && label == "dci recent" {
		return fmt.Errorf("%s trace missing user_query", label)
	}
	if trace.FinalEvidenceCount < 0 {
		return fmt.Errorf("%s trace has negative final_evidence_count", label)
	}
	seenSteps := map[int]struct{}{}
	for _, step := range trace.Steps {
		if step.StepNo <= 0 {
			return fmt.Errorf("%s step missing step_no", label)
		}
		if _, ok := seenSteps[step.StepNo]; ok {
			return fmt.Errorf("%s contains duplicate step_no %d", label, step.StepNo)
		}
		seenSteps[step.StepNo] = struct{}{}
		if strings.TrimSpace(step.Tool) == "" {
			return fmt.Errorf("%s step missing tool", label)
		}
		if strings.TrimSpace(step.Status) == "" {
			return fmt.Errorf("%s step missing status", label)
		}
		stepStatus := strings.TrimSpace(step.Status)
		if !isDCISearchStepStatus(stepStatus) {
			return fmt.Errorf("%s invalid step status %q", label, step.Status)
		}
		if stepStatus == "error" && strings.TrimSpace(step.ErrorMessage) == "" {
			return fmt.Errorf("%s error step missing error_message", label)
		}
		if step.ResultCount < 0 {
			return fmt.Errorf("%s step result_count must be >= 0", label)
		}
		if step.CreatedAt.IsZero() {
			return fmt.Errorf("%s step missing created_at", label)
		}
	}
	return nil
}

func isDCISearchTraceStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isDCISearchTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed":
		return true
	default:
		return false
	}
}

func isDCISearchStepStatus(status string) bool {
	switch status {
	case "ok", "error", "stopped", "completed":
		return true
	default:
		return false
	}
}

func validateDCIEvidence(evidence DCIEvidence) error {
	if strings.TrimSpace(evidence.EvidenceID) == "" {
		return fmt.Errorf("dci search response evidence missing evidence_id")
	}
	if strings.TrimSpace(evidence.FilePath) == "" {
		return fmt.Errorf("dci search response evidence missing file_path")
	}
	if evidence.LineStart <= 0 || evidence.LineEnd < evidence.LineStart {
		return fmt.Errorf("dci search response evidence has invalid line range")
	}
	if strings.TrimSpace(evidence.Snippet) == "" {
		return fmt.Errorf("dci search response evidence missing snippet")
	}
	if evidence.Confidence < 0 || evidence.Confidence > 1 {
		return fmt.Errorf("dci search response evidence confidence out of range")
	}
	return nil
}

func validateKnowledgeMemoryStatus(resp KnowledgeMemoryStatus) error {
	seenPersonal := map[string]struct{}{}
	for _, item := range resp.PersonalArchive {
		id := strings.TrimSpace(item.EntryID)
		if id == "" {
			return fmt.Errorf("knowledge memory status personal_archive missing entry_id")
		}
		if _, ok := seenPersonal[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate personal_archive entry_id %q", id)
		}
		seenPersonal[id] = struct{}{}
		if strings.TrimSpace(item.UserID) == "" {
			return fmt.Errorf("knowledge memory status personal_archive missing user_id")
		}
		if strings.TrimSpace(item.OriginalText) == "" {
			return fmt.Errorf("knowledge memory status personal_archive missing original_text")
		}
		if !item.Protected {
			return fmt.Errorf("knowledge memory status personal_archive original must be protected")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status personal_archive %q missing created_at", id)
		}
	}
	seenCreative := map[string]struct{}{}
	for _, item := range resp.CreativeKnowledge {
		id := strings.TrimSpace(item.ItemID)
		if id == "" {
			return fmt.Errorf("knowledge memory status creative_knowledge missing item_id")
		}
		if _, ok := seenCreative[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate creative_knowledge item_id %q", id)
		}
		seenCreative[id] = struct{}{}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("knowledge memory status creative_knowledge missing title")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("knowledge memory status creative_knowledge missing status")
		}
		if !isKnowledgeMemoryItemStatus(strings.TrimSpace(item.Status)) {
			return fmt.Errorf("knowledge memory status invalid creative_knowledge status=%q", item.Status)
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status creative_knowledge %q missing created_at", id)
		}
	}
	seenNews := map[string]struct{}{}
	for _, item := range resp.NewsKnowledge {
		id := strings.TrimSpace(item.ItemID)
		if id == "" {
			return fmt.Errorf("knowledge memory status news_knowledge missing item_id")
		}
		if _, ok := seenNews[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate news_knowledge item_id %q", id)
		}
		seenNews[id] = struct{}{}
		if strings.TrimSpace(item.Source) == "" {
			return fmt.Errorf("knowledge memory status news_knowledge missing source")
		}
		if strings.TrimSpace(item.Topic) == "" {
			return fmt.Errorf("knowledge memory status news_knowledge missing topic")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("knowledge memory status news_knowledge missing status")
		}
		if !isKnowledgeMemoryItemStatus(strings.TrimSpace(item.Status)) {
			return fmt.Errorf("knowledge memory status invalid news_knowledge status=%q", item.Status)
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status news_knowledge %q missing created_at", id)
		}
	}
	seenRules := map[string]struct{}{}
	for _, item := range resp.DailyIntakeRules {
		id := strings.TrimSpace(item.RuleID)
		if id == "" {
			return fmt.Errorf("knowledge memory status daily_intake_rule missing rule_id")
		}
		if _, ok := seenRules[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate daily_intake_rule rule_id %q", id)
		}
		seenRules[id] = struct{}{}
		if strings.TrimSpace(item.UserID) == "" {
			return fmt.Errorf("knowledge memory status daily_intake_rule missing user_id")
		}
		if strings.TrimSpace(item.Topic) == "" {
			return fmt.Errorf("knowledge memory status daily_intake_rule missing topic")
		}
		if strings.TrimSpace(item.Cadence) == "" {
			return fmt.Errorf("knowledge memory status daily_intake_rule missing cadence")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("knowledge memory status daily_intake_rule missing status")
		}
		if !isKnowledgeMemoryRuleStatus(strings.TrimSpace(item.Status)) {
			return fmt.Errorf("knowledge memory status invalid daily_intake_rule status=%q", item.Status)
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status daily_intake_rule %q missing created_at", id)
		}
	}
	seenMarkers := map[string]struct{}{}
	for _, item := range resp.TemporalMarkers {
		id := strings.TrimSpace(item.MarkerID)
		if id == "" {
			return fmt.Errorf("knowledge memory status temporal_marker missing marker_id")
		}
		if _, ok := seenMarkers[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate temporal_marker marker_id %q", id)
		}
		seenMarkers[id] = struct{}{}
		if strings.TrimSpace(item.Layer) == "" {
			return fmt.Errorf("knowledge memory status temporal_marker missing layer")
		}
		if !isKnowledgeMemoryTemporalLayer(strings.TrimSpace(item.Layer)) {
			return fmt.Errorf("knowledge memory status invalid temporal_marker layer=%q", item.Layer)
		}
		if strings.TrimSpace(item.ReferenceID) == "" {
			return fmt.Errorf("knowledge memory status temporal_marker missing reference_id")
		}
		if strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("knowledge memory status temporal_marker missing summary")
		}
		if item.AccessCount < 0 {
			return fmt.Errorf("knowledge memory status temporal_marker access_count must be >= 0")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status temporal_marker %q missing created_at", id)
		}
	}
	seenDreams := map[string]struct{}{}
	for _, item := range resp.DreamRuns {
		id := strings.TrimSpace(item.RunID)
		if id == "" {
			return fmt.Errorf("knowledge memory status dream_run missing run_id")
		}
		if _, ok := seenDreams[id]; ok {
			return fmt.Errorf("knowledge memory status contains duplicate dream_run run_id %q", id)
		}
		seenDreams[id] = struct{}{}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("knowledge memory status dream_run missing status")
		}
		if !isKnowledgeMemoryDreamStatus(strings.TrimSpace(item.Status)) {
			return fmt.Errorf("knowledge memory status invalid dream_run status=%q", item.Status)
		}
		if strings.TrimSpace(item.ReviewStatus) == "" {
			return fmt.Errorf("knowledge memory status dream_run missing review_status")
		}
		if !isKnowledgeMemoryReviewStatus(strings.TrimSpace(item.ReviewStatus)) {
			return fmt.Errorf("knowledge memory status invalid dream_run review_status=%q", item.ReviewStatus)
		}
		if err := validateKnowledgeMemoryDreamReviewConsistency(strings.TrimSpace(item.Status), strings.TrimSpace(item.ReviewStatus)); err != nil {
			return err
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("knowledge memory status dream_run %q missing created_at", id)
		}
	}
	return nil
}

func validateKnowledgeMemoryDreamReviewConsistency(status string, reviewStatus string) error {
	switch reviewStatus {
	case "pending":
		if status != "draft" && status != "proposal" {
			return fmt.Errorf("knowledge memory status dream_run pending review requires draft or proposal status")
		}
	case "approved":
		if status != "reviewed" && status != "promoted" {
			return fmt.Errorf("knowledge memory status dream_run cannot be auto-approved")
		}
	case "rejected":
		if status != "rejected" {
			return fmt.Errorf("knowledge memory status dream_run rejected review requires rejected status")
		}
	}
	return nil
}

func validateKnowledgeMemoryCreateResponse(resp KnowledgeMemoryCreateResponse) error {
	if strings.TrimSpace(resp.Status) != "created" {
		return fmt.Errorf("knowledge memory create response status mismatch")
	}
	return nil
}

func validateKnowledgeNewsItem(item KnowledgeNewsItem) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return fmt.Errorf("knowledge memory news item missing item_id")
	}
	if strings.TrimSpace(item.Source) == "" {
		return fmt.Errorf("knowledge memory news item missing source")
	}
	if strings.TrimSpace(item.Topic) == "" {
		return fmt.Errorf("knowledge memory news item missing topic")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("knowledge memory news item missing status")
	}
	if !isKnowledgeMemoryItemStatus(strings.TrimSpace(item.Status)) {
		return fmt.Errorf("knowledge memory news item invalid status=%q", item.Status)
	}
	return nil
}

func validateKnowledgeDailyIntakeRule(item KnowledgeDailyIntakeRule) error {
	if strings.TrimSpace(item.RuleID) == "" {
		return fmt.Errorf("knowledge memory daily intake rule missing rule_id")
	}
	if strings.TrimSpace(item.UserID) == "" {
		return fmt.Errorf("knowledge memory daily intake rule missing user_id")
	}
	if strings.TrimSpace(item.Topic) == "" {
		return fmt.Errorf("knowledge memory daily intake rule missing topic")
	}
	if strings.TrimSpace(item.Cadence) == "" {
		return fmt.Errorf("knowledge memory daily intake rule missing cadence")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("knowledge memory daily intake rule missing status")
	}
	if !isKnowledgeMemoryRuleStatus(strings.TrimSpace(item.Status)) {
		return fmt.Errorf("knowledge memory daily intake rule invalid status=%q", item.Status)
	}
	return nil
}

func isKnowledgeMemoryItemStatus(status string) bool {
	switch status {
	case "candidate", "reviewed", "promoted", "rejected":
		return true
	default:
		return false
	}
}

func isKnowledgeMemoryRuleStatus(status string) bool {
	switch status {
	case "candidate", "reviewed", "enabled", "active", "rejected":
		return true
	default:
		return false
	}
}

func isKnowledgeMemoryTemporalLayer(layer string) bool {
	switch layer {
	case "thread", "today", "3days", "week", "month", "year", "long_term":
		return true
	default:
		return false
	}
}

func isKnowledgeMemoryDreamStatus(status string) bool {
	switch status {
	case "draft", "proposal", "reviewed", "promoted", "rejected":
		return true
	default:
		return false
	}
}

func isKnowledgeMemoryReviewStatus(status string) bool {
	switch status {
	case "pending", "approved", "rejected":
		return true
	default:
		return false
	}
}

func validateKnowledgeMemoryReviewRequest(req KnowledgeMemoryReviewRequest) error {
	if strings.TrimSpace(req.DetailType) == "" {
		return fmt.Errorf("knowledge memory review request missing detail_type")
	}
	if strings.TrimSpace(req.ID) == "" {
		return fmt.Errorf("knowledge memory review request missing id")
	}
	switch strings.TrimSpace(req.ReviewStatus) {
	case "approved", "rejected":
	default:
		return fmt.Errorf("knowledge memory review request review_status must be approved or rejected")
	}
	if req.Promote && strings.TrimSpace(req.ReviewStatus) != "approved" {
		return fmt.Errorf("knowledge memory review request promote requires approved review_status")
	}
	return nil
}

func validateKnowledgeMemoryReviewResponse(resp KnowledgeMemoryReviewResponse, req KnowledgeMemoryReviewRequest) error {
	if resp.Status != "reviewed" {
		return fmt.Errorf("knowledge memory review response status mismatch")
	}
	if resp.DetailType != strings.TrimSpace(req.DetailType) {
		return fmt.Errorf("knowledge memory review response detail_type mismatch")
	}
	if resp.ID != strings.TrimSpace(req.ID) {
		return fmt.Errorf("knowledge memory review response id mismatch")
	}
	if resp.ReviewStatus != strings.TrimSpace(req.ReviewStatus) {
		return fmt.Errorf("knowledge memory review response review_status mismatch")
	}
	if resp.Promoted != req.Promote {
		return fmt.Errorf("knowledge memory review response promoted mismatch")
	}
	if resp.AutoPromote {
		return fmt.Errorf("knowledge memory review response unexpectedly auto_promoted")
	}
	if strings.TrimSpace(resp.Comparison.TargetStatus) == "" {
		return fmt.Errorf("knowledge memory review response comparison missing target_status")
	}
	if strings.TrimSpace(resp.Comparison.FormalTarget) == "" {
		return fmt.Errorf("knowledge memory review response comparison missing formal_target")
	}
	if req.Promote && resp.Comparison.TargetStatus != expectedKnowledgeMemoryPromotedStatus(req.DetailType) {
		return fmt.Errorf("knowledge memory review response target_status mismatch")
	}
	if !req.Promote && req.ReviewStatus == "approved" && resp.Comparison.TargetStatus != "reviewed" {
		return fmt.Errorf("knowledge memory review response target_status mismatch")
	}
	if req.ReviewStatus == "rejected" && resp.Comparison.TargetStatus != "rejected" {
		return fmt.Errorf("knowledge memory review response target_status mismatch")
	}
	return nil
}

func expectedKnowledgeMemoryPromotedStatus(detailType string) string {
	if strings.TrimSpace(detailType) == "daily_intake_rule" {
		return "enabled"
	}
	return "promoted"
}

func validateSourceRegistryStatus(resp SourceRegistryStatus) error {
	seen := map[string]struct{}{}
	for _, item := range resp.Entries {
		id := strings.TrimSpace(item.SourceID)
		if id == "" {
			return fmt.Errorf("source registry status entry missing source_id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("source registry status contains duplicate source_id %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.URL) == "" {
			return fmt.Errorf("source registry status entry missing url")
		}
		if strings.TrimSpace(item.Kind) == "" {
			return fmt.Errorf("source registry status entry missing kind")
		}
		if !isSourceRegistryKind(strings.TrimSpace(item.Kind)) {
			return fmt.Errorf("source registry status entry invalid kind=%q", item.Kind)
		}
		if item.TrustScore < 0 || item.TrustScore > 1 {
			return fmt.Errorf("source registry status entry trust_score out of range")
		}
		if item.FetchIntervalSec < 0 {
			return fmt.Errorf("source registry status entry fetch_interval_sec must be >= 0")
		}
		if strings.TrimSpace(item.CreatedAt) == "" {
			return fmt.Errorf("source registry status entry missing created_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt)); err != nil {
			return fmt.Errorf("source registry status entry invalid created_at: %w", err)
		}
		if strings.TrimSpace(item.UpdatedAt) == "" {
			return fmt.Errorf("source registry status entry missing updated_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.UpdatedAt)); err != nil {
			return fmt.Errorf("source registry status entry invalid updated_at: %w", err)
		}
		lastStatus := strings.TrimSpace(item.LastStatus)
		if lastStatus != "" && !isSourceRegistryFetchStatus(lastStatus) {
			return fmt.Errorf("source registry status entry invalid last_status=%q", item.LastStatus)
		}
		if lastStatus != "" && strings.TrimSpace(item.LastFetchedAt) == "" {
			return fmt.Errorf("source registry status entry last_status missing last_fetched_at")
		}
		if lastStatus == "error" && strings.TrimSpace(item.LastError) == "" {
			return fmt.Errorf("source registry status entry last_status=error missing last_error")
		}
		if lastStatus == "" && strings.TrimSpace(item.LastError) != "" {
			return fmt.Errorf("source registry status entry last_error without last_status")
		}
	}
	return nil
}

func validateMemoryLayersStatus(resp MemoryLayersStatus) error {
	for _, item := range resp.L0 {
		if err := validateMemoryLayerEvent("l0", item); err != nil {
			return err
		}
	}
	for _, item := range resp.L1 {
		if err := validateMemoryLayerEvent("l1", item); err != nil {
			return err
		}
	}
	for _, item := range resp.L3 {
		if err := validateMemoryLayerEvent("l3", item); err != nil {
			return err
		}
	}
	for _, item := range resp.L2 {
		if item.ThreadID <= 0 {
			return fmt.Errorf("l2 summary missing thread_id")
		}
		if strings.TrimSpace(item.Summary) == "" {
			return fmt.Errorf("l2 summary missing summary")
		}
		if strings.TrimSpace(item.StartTime) != "" {
			if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.StartTime)); err != nil {
				return fmt.Errorf("l2 summary invalid ts_start: %w", err)
			}
		}
		if strings.TrimSpace(item.EndTime) != "" {
			if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.EndTime)); err != nil {
				return fmt.Errorf("l2 summary invalid ts_end: %w", err)
			}
		}
	}
	for _, item := range resp.L3Qdrant {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("l3_qdrant document missing id")
		}
		if strings.TrimSpace(item.Domain) == "" {
			return fmt.Errorf("l3_qdrant document missing domain")
		}
		if strings.TrimSpace(item.Content) == "" {
			return fmt.Errorf("l3_qdrant document missing content")
		}
		if strings.TrimSpace(item.CreatedAt) != "" {
			if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt)); err != nil {
				return fmt.Errorf("l3_qdrant document invalid created_at: %w", err)
			}
		}
		if strings.TrimSpace(item.UpdatedAt) != "" {
			if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.UpdatedAt)); err != nil {
				return fmt.Errorf("l3_qdrant document invalid updated_at: %w", err)
			}
		}
	}
	return nil
}

func validateDomainGraphAssertionsResponse(resp DomainGraphAssertionsResponse) error {
	if resp.Limit < 0 {
		return fmt.Errorf("domain graph assertions response limit must be >= 0")
	}
	if resp.Offset < 0 {
		return fmt.Errorf("domain graph assertions response offset must be >= 0")
	}
	if resp.Total < 0 {
		return fmt.Errorf("domain graph assertions response total must be >= 0")
	}
	seen := map[string]struct{}{}
	for _, item := range resp.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return fmt.Errorf("domain graph assertion missing id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("domain graph assertions response contains duplicate id %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.StagingID) == "" {
			return fmt.Errorf("domain graph assertion missing staging_id")
		}
		if strings.TrimSpace(item.Domain) == "" {
			return fmt.Errorf("domain graph assertion missing domain")
		}
		if strings.TrimSpace(item.EntityType) == "" {
			return fmt.Errorf("domain graph assertion missing entity_type")
		}
		if strings.TrimSpace(item.SourceID) == "" {
			return fmt.Errorf("domain graph assertion missing source_id")
		}
		if strings.TrimSpace(item.RawHash) == "" {
			return fmt.Errorf("domain graph assertion missing raw_hash")
		}
		if item.Confidence < 0 || item.Confidence > 1 {
			return fmt.Errorf("domain graph assertion confidence out of range")
		}
		status := strings.TrimSpace(item.ValidationStatus)
		if status == "" {
			return fmt.Errorf("domain graph assertion missing validation_status")
		}
		if !isSourceRegistryStagingStatus(status) {
			return fmt.Errorf("domain graph assertion invalid validation_status=%q", item.ValidationStatus)
		}
		if item.Evidence == nil {
			return fmt.Errorf("domain graph assertion missing evidence")
		}
		if strings.TrimSpace(item.CreatedAt) == "" {
			return fmt.Errorf("domain graph assertion missing created_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt)); err != nil {
			return fmt.Errorf("domain graph assertion invalid created_at: %w", err)
		}
		if strings.TrimSpace(item.UpdatedAt) == "" {
			return fmt.Errorf("domain graph assertion missing updated_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.UpdatedAt)); err != nil {
			return fmt.Errorf("domain graph assertion invalid updated_at: %w", err)
		}
	}
	return nil
}

func validateMemoryLayerEvent(layer string, item MemoryLayerEvent) error {
	if strings.TrimSpace(item.ID) == "" {
		return fmt.Errorf("%s memory missing id", layer)
	}
	if strings.TrimSpace(item.Message) == "" {
		return fmt.Errorf("%s memory missing message", layer)
	}
	if strings.TrimSpace(item.Layer) == "" {
		return fmt.Errorf("%s memory missing layer", layer)
	}
	if strings.TrimSpace(item.CreatedAt) == "" {
		return fmt.Errorf("%s memory missing created_at", layer)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt)); err != nil {
		return fmt.Errorf("%s memory invalid created_at: %w", layer, err)
	}
	if strings.TrimSpace(item.UpdatedAt) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.UpdatedAt)); err != nil {
			return fmt.Errorf("%s memory invalid updated_at: %w", layer, err)
		}
	}
	return nil
}

func validateSourceRegistryStagingStatus(resp SourceRegistryStagingStatus) error {
	seen := map[string]struct{}{}
	for _, item := range resp.Items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			return fmt.Errorf("source registry staging item missing id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("source registry staging contains duplicate id %q", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(item.Kind) == "" {
			return fmt.Errorf("source registry staging item missing kind")
		}
		if strings.TrimSpace(item.Namespace) == "" {
			return fmt.Errorf("source registry staging item missing namespace")
		}
		if strings.TrimSpace(item.SourceID) == "" {
			return fmt.Errorf("source registry staging item missing source_id")
		}
		if strings.TrimSpace(item.SourceURL) == "" {
			return fmt.Errorf("source registry staging item missing source_url")
		}
		if strings.TrimSpace(item.RawText) == "" {
			return fmt.Errorf("source registry staging item missing raw_text")
		}
		if strings.TrimSpace(item.ValidationStatus) == "" {
			return fmt.Errorf("source registry staging item missing validation_status")
		}
		if !isSourceRegistryStagingStatus(strings.TrimSpace(item.ValidationStatus)) {
			return fmt.Errorf("source registry staging item invalid validation_status=%q", item.ValidationStatus)
		}
		if strings.TrimSpace(item.CreatedAt) == "" {
			return fmt.Errorf("source registry staging item missing created_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.CreatedAt)); err != nil {
			return fmt.Errorf("source registry staging item invalid created_at: %w", err)
		}
		if strings.TrimSpace(item.UpdatedAt) == "" {
			return fmt.Errorf("source registry staging item missing updated_at")
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(item.UpdatedAt)); err != nil {
			return fmt.Errorf("source registry staging item invalid updated_at: %w", err)
		}
		if err := validateSourceRegistryStagingTerminalEvidence(item); err != nil {
			return err
		}
	}
	return nil
}

func validateSourceRegistryStagingTerminalEvidence(item SourceRegistryStagingItem) error {
	status := strings.TrimSpace(item.ValidationStatus)
	if status == "pending" {
		return nil
	}
	if !jsonMapHasNonEmptyString(item.Meta, "validated_at") {
		return fmt.Errorf("source registry staging item terminal validation_status missing validated_at")
	}
	if status == "rejected" && !jsonMapHasNonEmptyArray(item.Meta, "validation_issues") {
		return fmt.Errorf("source registry staging item rejected validation_status missing validation_issues")
	}
	return nil
}

func validateSourceRegistryValidateRequest(req SourceRegistryValidateRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return fmt.Errorf("source registry validation request missing id")
	}
	if req.MinimumTrustScore != nil && (*req.MinimumTrustScore < 0 || *req.MinimumTrustScore > 1) {
		return fmt.Errorf("source registry validation request minimum_trust_score out of range")
	}
	return nil
}

func validateSourceRegistryValidationResponse(resp SourceRegistryValidationResponse, req SourceRegistryValidateRequest) error {
	if resp.Result.ItemID != strings.TrimSpace(req.ID) {
		return fmt.Errorf("source registry validation response item_id mismatch")
	}
	status := strings.TrimSpace(resp.Result.Status)
	if status == "" {
		return fmt.Errorf("source registry validation response missing status")
	}
	if !isSourceRegistryStagingStatus(status) {
		return fmt.Errorf("source registry validation response invalid status=%q", resp.Result.Status)
	}
	if status == "pending" {
		return fmt.Errorf("source registry validation response non-terminal status=%q", resp.Result.Status)
	}
	if resp.Result.Passed {
		if status != "validated" {
			return fmt.Errorf("source registry validation response passed without validated status")
		}
		if len(resp.Result.Issues) > 0 {
			return fmt.Errorf("source registry validation response passed with issues")
		}
	} else {
		if status == "validated" {
			return fmt.Errorf("source registry validation response validated status without passed=true")
		}
		if len(resp.Result.Issues) == 0 {
			return fmt.Errorf("source registry validation response failed without issues")
		}
	}
	if resp.Result.PromotedMemoryID != "" && !req.AutoPromoteMemoryCandidate {
		return fmt.Errorf("source registry validation response auto-promoted without request")
	}
	return nil
}

func isSourceRegistryKind(kind string) bool {
	switch kind {
	case "rss", "atom", "official_api", "github", "huggingface", "pypi", "mediawiki", "search_fallback":
		return true
	default:
		return false
	}
}

func isSourceRegistryFetchStatus(status string) bool {
	switch status {
	case "ok", "error":
		return true
	default:
		return false
	}
}

func isSourceRegistryStagingStatus(status string) bool {
	switch status {
	case "pending", "validated", "rejected":
		return true
	default:
		return false
	}
}

func validateSourceRegistryPromoteRequest(req SourceRegistryPromoteRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return fmt.Errorf("source registry promotion request missing id")
	}
	switch strings.TrimSpace(req.Target) {
	case "news":
		if strings.TrimSpace(req.Category) == "" {
			return fmt.Errorf("source registry promotion request missing category")
		}
	case "knowledge":
		if strings.TrimSpace(req.Domain) == "" {
			return fmt.Errorf("source registry promotion request missing domain")
		}
	case "domain_graph":
		if strings.TrimSpace(req.Domain) == "" {
			return fmt.Errorf("source registry promotion request missing domain")
		}
		if strings.TrimSpace(req.EntityType) == "" {
			return fmt.Errorf("source registry promotion request missing entity_type")
		}
		if req.Confidence != nil && (*req.Confidence <= 0 || *req.Confidence > 1) {
			return fmt.Errorf("source registry promotion request confidence out of range")
		}
	case "memory":
		if strings.TrimSpace(req.TargetNamespace) == "" {
			return fmt.Errorf("source registry promotion request missing target_namespace")
		}
	default:
		return fmt.Errorf("source registry promotion request target must be news, knowledge, domain_graph, or memory")
	}
	return nil
}

func validateSourceRegistryPromotionResponse(resp SourceRegistryPromotionResponse, req SourceRegistryPromoteRequest) error {
	if resp.Target != strings.TrimSpace(req.Target) {
		return fmt.Errorf("source registry promotion response target mismatch")
	}
	if len(resp.Item) == 0 {
		return fmt.Errorf("source registry promotion response missing item")
	}
	if !jsonMapHasNonEmptyString(resp.Item, "ID") {
		return fmt.Errorf("source registry promotion response item missing id")
	}
	switch req.Target {
	case "news":
		if !jsonMapStringEquals(resp.Item, "StagingID", strings.TrimSpace(req.ID)) {
			return fmt.Errorf("source registry promotion response staging_id mismatch")
		}
		if !jsonMapStringEquals(resp.Item, "Category", strings.TrimSpace(req.Category)) {
			return fmt.Errorf("source registry promotion response category mismatch")
		}
		if err := validateJSONMapRFC3339Time(resp.Item, "CreatedAt", "source registry promotion response item"); err != nil {
			return err
		}
	case "knowledge":
		if !jsonMapStringEquals(resp.Item, "StagingID", strings.TrimSpace(req.ID)) {
			return fmt.Errorf("source registry promotion response staging_id mismatch")
		}
		if !jsonMapStringEquals(resp.Item, "Domain", strings.TrimSpace(req.Domain)) {
			return fmt.Errorf("source registry promotion response domain mismatch")
		}
		if err := validateJSONMapRFC3339Time(resp.Item, "CreatedAt", "source registry promotion response item"); err != nil {
			return err
		}
	case "domain_graph":
		if !jsonMapStringEquals(resp.Item, "StagingID", strings.TrimSpace(req.ID)) {
			return fmt.Errorf("source registry promotion response staging_id mismatch")
		}
		if !jsonMapStringEquals(resp.Item, "Domain", normalizeSourceRegistryPromotionToken(req.Domain)) {
			return fmt.Errorf("source registry promotion response domain mismatch")
		}
		if !jsonMapStringEquals(resp.Item, "EntityType", normalizeSourceRegistryPromotionToken(req.EntityType)) {
			return fmt.Errorf("source registry promotion response entity_type mismatch")
		}
		if err := validateJSONMapRFC3339Time(resp.Item, "CreatedAt", "source registry promotion response item"); err != nil {
			return err
		}
	case "memory":
		if !jsonMapNestedStringEquals(resp.Item, "Meta", "staging_id", strings.TrimSpace(req.ID)) {
			return fmt.Errorf("source registry promotion response staging_id mismatch")
		}
		if !jsonMapStringEquals(resp.Item, "Namespace", strings.TrimSpace(req.TargetNamespace)) {
			return fmt.Errorf("source registry promotion response namespace mismatch")
		}
		if err := validateJSONMapRFC3339Time(resp.Item, "CreatedAt", "source registry promotion response item"); err != nil {
			return err
		}
	}
	return nil
}

func normalizeSourceRegistryPromotionToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func validateJSONMapRFC3339Time(item map[string]any, key string, label string) error {
	raw, ok := item[key]
	if !ok {
		return fmt.Errorf("%s missing %s", label, jsonMapTimeFieldName(key))
	}
	value := strings.TrimSpace(fmt.Sprint(raw))
	if value == "" {
		return fmt.Errorf("%s missing %s", label, jsonMapTimeFieldName(key))
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%s invalid %s: %w", label, jsonMapTimeFieldName(key), err)
	}
	return nil
}

func jsonMapTimeFieldName(key string) string {
	switch key {
	case "CreatedAt":
		return "created_at"
	case "UpdatedAt":
		return "updated_at"
	default:
		return strings.ToLower(key)
	}
}

func jsonMapStringEquals(item map[string]any, key string, want string) bool {
	got, ok := item[key]
	if !ok {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(got)) == want
}

func jsonMapHasNonEmptyString(item map[string]any, key string) bool {
	got, ok := item[key]
	if !ok {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(got)) != ""
}

func jsonMapHasNonEmptyArray(item map[string]any, key string) bool {
	got, ok := item[key]
	if !ok {
		return false
	}
	switch v := got.(type) {
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	default:
		return false
	}
}

func jsonMapNestedStringEquals(item map[string]any, parent string, key string, want string) bool {
	raw, ok := item[parent]
	if !ok {
		return false
	}
	nested, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	return jsonMapStringEquals(nested, key, want)
}

func validateBrowserTraceAPIStatus(resp BrowserTraceAPIStatus) error {
	seenRuns := map[string]struct{}{}
	for _, item := range resp.TraceRuns {
		if err := validateBrowserTraceRun(item, "browser trace api status"); err != nil {
			return err
		}
		id := strings.TrimSpace(item.TraceRunID)
		if _, ok := seenRuns[id]; ok {
			return fmt.Errorf("browser trace api status contains duplicate trace_run_id %q", id)
		}
		seenRuns[id] = struct{}{}
	}
	seenCandidates := map[string]struct{}{}
	for _, item := range resp.APICandidates {
		if err := validateBrowserTraceAPICandidate(item, "browser trace api status"); err != nil {
			return err
		}
		id := strings.TrimSpace(item.CandidateID)
		if _, ok := seenCandidates[id]; ok {
			return fmt.Errorf("browser trace api status contains duplicate candidate_id %q", id)
		}
		seenCandidates[id] = struct{}{}
	}
	seenSchemas := map[string]struct{}{}
	for _, item := range resp.APISchemas {
		if err := validateBrowserTraceAPISchema(item, "browser trace api status"); err != nil {
			return err
		}
		id := strings.TrimSpace(item.SchemaID)
		if _, ok := seenSchemas[id]; ok {
			return fmt.Errorf("browser trace api status contains duplicate schema_id %q", id)
		}
		seenSchemas[id] = struct{}{}
	}
	seenValidations := map[string]struct{}{}
	for _, item := range resp.APIValidations {
		if err := validateBrowserTraceAPIValidation(item, "browser trace api status"); err != nil {
			return err
		}
		id := strings.TrimSpace(item.ValidationID)
		if _, ok := seenValidations[id]; ok {
			return fmt.Errorf("browser trace api status contains duplicate validation_id %q", id)
		}
		seenValidations[id] = struct{}{}
	}
	seenCoverage := map[string]struct{}{}
	for _, item := range resp.CoverageReports {
		if err := validateBrowserTraceAPICoverage(item, "browser trace api status"); err != nil {
			return err
		}
		if _, ok := seenCoverage[item.ReportID]; ok {
			return fmt.Errorf("browser trace api status contains duplicate coverage report_id %q", item.ReportID)
		}
		seenCoverage[item.ReportID] = struct{}{}
	}
	seenArtifacts := map[string]struct{}{}
	for _, item := range resp.APIArtifacts {
		if err := validateBrowserTraceAPIArtifact(item, "browser trace api status"); err != nil {
			return err
		}
		id := strings.TrimSpace(item.ArtifactID)
		if _, ok := seenArtifacts[id]; ok {
			return fmt.Errorf("browser trace api status contains duplicate artifact_id %q", id)
		}
		seenArtifacts[id] = struct{}{}
	}
	return nil
}

func validateBrowserTraceAPIDiscoverRequest(req BrowserTraceAPIDiscoverRequest) error {
	if strings.TrimSpace(req.TraceRunID) == "" {
		return fmt.Errorf("browser trace api discover request missing trace_run_id")
	}
	if strings.TrimSpace(req.TracePath) == "" {
		return fmt.Errorf("browser trace api discover request missing trace_path")
	}
	if strings.TrimSpace(req.RequestsPath) == "" {
		return fmt.Errorf("browser trace api discover request missing requests_path")
	}
	if strings.TrimSpace(req.ResponsesPath) == "" {
		return fmt.Errorf("browser trace api discover request missing responses_path")
	}
	return nil
}

func validateBrowserTraceAPIDiscoverResponse(resp BrowserTraceAPIDiscoverResponse, req BrowserTraceAPIDiscoverRequest) error {
	if resp.TraceRun.TraceRunID != strings.TrimSpace(req.TraceRunID) {
		return fmt.Errorf("browser trace api discover response trace_run_id mismatch")
	}
	if resp.TraceRun.TracePath != strings.TrimSpace(req.TracePath) {
		return fmt.Errorf("browser trace api discover response trace_path mismatch")
	}
	if err := validateBrowserTraceRun(resp.TraceRun, "browser trace api discover response"); err != nil {
		return err
	}
	for _, item := range resp.APICandidates {
		if item.TraceRunID != resp.TraceRun.TraceRunID {
			return fmt.Errorf("browser trace api discover response candidate trace_run_id mismatch")
		}
		if err := validateBrowserTraceAPICandidate(item, "browser trace api discover response"); err != nil {
			return err
		}
	}
	for _, item := range resp.APISchemas {
		if err := validateBrowserTraceAPISchema(item, "browser trace api discover response"); err != nil {
			return err
		}
	}
	for _, item := range resp.APIValidations {
		if item.TraceRunID != resp.TraceRun.TraceRunID {
			return fmt.Errorf("browser trace api discover response validation trace_run_id mismatch")
		}
		if err := validateBrowserTraceAPIValidation(item, "browser trace api discover response"); err != nil {
			return err
		}
	}
	if resp.CoverageReport.TraceRunID != resp.TraceRun.TraceRunID {
		return fmt.Errorf("browser trace api discover response coverage trace_run_id mismatch")
	}
	if err := validateBrowserTraceAPICoverage(resp.CoverageReport, "browser trace api discover response"); err != nil {
		return err
	}
	for _, item := range resp.APIArtifacts {
		if item.TraceRunID != resp.TraceRun.TraceRunID {
			return fmt.Errorf("browser trace api discover response artifact trace_run_id mismatch")
		}
		if err := validateBrowserTraceAPIArtifact(item, "browser trace api discover response"); err != nil {
			return err
		}
	}
	return nil
}

func validateBrowserTraceAPIFetcherProposalRequest(req BrowserTraceAPIFetcherProposalRequest) error {
	if strings.TrimSpace(req.CandidateID) == "" {
		return fmt.Errorf("browser trace api fetcher proposal request missing candidate_id")
	}
	if !req.HumanApproved {
		return fmt.Errorf("browser trace api fetcher proposal request requires human_approved")
	}
	return nil
}

func validateBrowserTraceAPIValidationReviewRequest(req BrowserTraceAPIValidationReviewRequest) error {
	if strings.TrimSpace(req.CandidateID) == "" {
		return fmt.Errorf("browser trace api validation review request missing candidate_id")
	}
	if strings.TrimSpace(req.Reviewer) == "" {
		return fmt.Errorf("browser trace api validation review request missing reviewer")
	}
	return nil
}

func validateBrowserTraceAPIValidationReviewResponse(resp BrowserTraceAPIValidationReviewResponse, req BrowserTraceAPIValidationReviewRequest) error {
	if resp.OfficialPromotion {
		return fmt.Errorf("browser trace api validation review response unexpectedly official_promotion")
	}
	if resp.ImplementationApply {
		return fmt.Errorf("browser trace api validation review response unexpectedly implementation_apply")
	}
	if resp.Candidate.CandidateID != strings.TrimSpace(req.CandidateID) {
		return fmt.Errorf("browser trace api validation review response candidate_id mismatch")
	}
	if err := validateBrowserTraceAPICandidate(resp.Candidate, "browser trace api validation review response"); err != nil {
		return err
	}
	if resp.Validation.CandidateID != strings.TrimSpace(req.CandidateID) {
		return fmt.Errorf("browser trace api validation review response validation candidate_id mismatch")
	}
	if resp.Validation.TraceRunID != resp.Candidate.TraceRunID {
		return fmt.Errorf("browser trace api validation review response validation trace_run_id mismatch")
	}
	if err := validateBrowserTraceAPIValidation(resp.Validation, "browser trace api validation review response"); err != nil {
		return err
	}
	if req.HumanApproved && req.TermsReviewed && req.OfficialAPIReviewed && req.PIIReviewed && req.SchemaReviewed && req.RiskReviewed {
		if !resp.Validation.Passed || resp.Validation.Status != "validated" {
			return fmt.Errorf("browser trace api validation review response expected validated result")
		}
	}
	return nil
}

func validateBrowserTraceAPIFetcherProposalResponse(resp BrowserTraceAPIFetcherProposalResponse, req BrowserTraceAPIFetcherProposalRequest) error {
	if resp.OfficialPromotion {
		return fmt.Errorf("browser trace api fetcher proposal response unexpectedly official_promotion")
	}
	if resp.ImplementationApply {
		return fmt.Errorf("browser trace api fetcher proposal response unexpectedly implementation_apply")
	}
	if resp.Candidate.CandidateID != strings.TrimSpace(req.CandidateID) {
		return fmt.Errorf("browser trace api fetcher proposal response candidate_id mismatch")
	}
	if err := validateBrowserTraceAPICandidate(resp.Candidate, "browser trace api fetcher proposal response"); err != nil {
		return err
	}
	if resp.Validation.CandidateID != strings.TrimSpace(req.CandidateID) {
		return fmt.Errorf("browser trace api fetcher proposal response validation candidate_id mismatch")
	}
	if !resp.Validation.Passed || resp.Validation.Status != "validated" {
		return fmt.Errorf("browser trace api fetcher proposal response requires validated candidate")
	}
	if err := validateBrowserTraceAPIValidation(resp.Validation, "browser trace api fetcher proposal response"); err != nil {
		return err
	}
	if resp.APIArtifact.Type != "fetcher_proposal" {
		return fmt.Errorf("browser trace api fetcher proposal response artifact type mismatch")
	}
	if resp.APIArtifact.Status != "pending_review" {
		return fmt.Errorf("browser trace api fetcher proposal response artifact status mismatch")
	}
	if err := validateBrowserTraceAPIArtifact(resp.APIArtifact, "browser trace api fetcher proposal response"); err != nil {
		return err
	}
	if req.WorkstreamID != "" {
		if resp.WorkstreamArtifact == nil {
			return fmt.Errorf("browser trace api fetcher proposal response missing workstream artifact")
		}
		if resp.WorkstreamArtifact.WorkstreamID != req.WorkstreamID {
			return fmt.Errorf("browser trace api fetcher proposal response workstream_id mismatch")
		}
		if resp.WorkstreamArtifact.Status != "pending_review" {
			return fmt.Errorf("browser trace api fetcher proposal response workstream artifact status mismatch")
		}
		if resp.WorkstreamArtifact.CreatedAt.IsZero() {
			return fmt.Errorf("browser trace api fetcher proposal response workstream artifact missing created_at")
		}
	}
	return nil
}

func validateBrowserTraceRun(item BrowserTraceRun, label string) error {
	if strings.TrimSpace(item.TraceRunID) == "" {
		return fmt.Errorf("%s trace_run missing trace_run_id", label)
	}
	if strings.TrimSpace(item.TracePath) == "" {
		return fmt.Errorf("%s trace_run missing trace_path", label)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s trace_run missing created_at", label)
	}
	return nil
}

func validateBrowserTraceAPICandidate(item BrowserTraceAPICandidate, label string) error {
	if strings.TrimSpace(item.CandidateID) == "" {
		return fmt.Errorf("%s candidate missing candidate_id", label)
	}
	if strings.TrimSpace(item.TraceRunID) == "" {
		return fmt.Errorf("%s candidate missing trace_run_id", label)
	}
	method := strings.ToUpper(strings.TrimSpace(item.Method))
	if method == "" {
		return fmt.Errorf("%s candidate missing method", label)
	}
	if method == "PUT" || method == "PATCH" || method == "DELETE" {
		return fmt.Errorf("%s candidate uses write method", label)
	}
	if strings.TrimSpace(item.ObservedURL) == "" {
		return fmt.Errorf("%s candidate missing observed_url", label)
	}
	if strings.TrimSpace(item.ContainsPersonalData) == "" {
		return fmt.Errorf("%s candidate missing contains_personal_data", label)
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("%s candidate missing status", label)
	}
	if !isBrowserTraceAPICandidateStatus(strings.TrimSpace(item.Status)) {
		return fmt.Errorf("%s candidate status invalid %q", label, item.Status)
	}
	if item.Confidence < 0 || item.Confidence > 1 {
		return fmt.Errorf("%s candidate confidence out of range", label)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s candidate missing created_at", label)
	}
	return nil
}

func validateBrowserTraceAPISchema(item BrowserTraceAPISchema, label string) error {
	if strings.TrimSpace(item.SchemaID) == "" {
		return fmt.Errorf("%s schema missing schema_id", label)
	}
	if strings.TrimSpace(item.CandidateID) == "" {
		return fmt.Errorf("%s schema missing candidate_id", label)
	}
	if strings.TrimSpace(item.SchemaType) == "" {
		return fmt.Errorf("%s schema missing schema_type", label)
	}
	if strings.TrimSpace(item.SchemaJSON) == "" {
		return fmt.Errorf("%s schema missing schema_json", label)
	}
	if !json.Valid([]byte(item.SchemaJSON)) {
		return fmt.Errorf("%s schema_json must be valid json", label)
	}
	if item.SampleCount < 0 {
		return fmt.Errorf("%s schema sample_count must be >= 0", label)
	}
	if item.Confidence < 0 || item.Confidence > 1 {
		return fmt.Errorf("%s schema confidence out of range", label)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s schema missing created_at", label)
	}
	return nil
}

func validateBrowserTraceAPIValidation(item BrowserTraceAPIValidation, label string) error {
	if strings.TrimSpace(item.ValidationID) == "" {
		return fmt.Errorf("%s validation missing validation_id", label)
	}
	if strings.TrimSpace(item.CandidateID) == "" {
		return fmt.Errorf("%s validation missing candidate_id", label)
	}
	if strings.TrimSpace(item.TraceRunID) == "" {
		return fmt.Errorf("%s validation missing trace_run_id", label)
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("%s validation missing status", label)
	}
	if !isBrowserTraceAPIValidationStatus(strings.TrimSpace(item.Status)) {
		return fmt.Errorf("%s validation status invalid %q", label, item.Status)
	}
	if item.Passed && item.Status != "validated" {
		return fmt.Errorf("%s validation passed without validated status", label)
	}
	if item.Status == "validated" {
		if !item.Passed {
			return fmt.Errorf("%s validation validated status without passed=true", label)
		}
		if len(item.Issues) > 0 {
			return fmt.Errorf("%s validation validated status with issues", label)
		}
	}
	if item.Status == "needs_review" && item.Passed {
		return fmt.Errorf("%s validation needs_review status with passed=true", label)
	}
	if !item.Passed && len(item.Issues) == 0 {
		return fmt.Errorf("%s validation failed without issues", label)
	}
	for _, issue := range item.Issues {
		if strings.TrimSpace(issue.Code) == "" {
			return fmt.Errorf("%s validation issue missing code", label)
		}
		if strings.TrimSpace(issue.Message) == "" {
			return fmt.Errorf("%s validation issue missing message", label)
		}
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s validation missing created_at", label)
	}
	return nil
}

func validateBrowserTraceAPICoverage(item BrowserTraceAPICoverage, label string) error {
	if strings.TrimSpace(item.ReportID) == "" {
		return fmt.Errorf("%s coverage missing report_id", label)
	}
	if strings.TrimSpace(item.TraceRunID) == "" {
		return fmt.Errorf("%s coverage missing trace_run_id", label)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s coverage missing created_at", label)
	}
	return nil
}

func validateBrowserTraceAPIArtifact(item BrowserTraceAPIArtifact, label string) error {
	if strings.TrimSpace(item.ArtifactID) == "" {
		return fmt.Errorf("%s artifact missing artifact_id", label)
	}
	if strings.TrimSpace(item.TraceRunID) == "" {
		return fmt.Errorf("%s artifact missing trace_run_id", label)
	}
	if strings.TrimSpace(item.Type) == "" {
		return fmt.Errorf("%s artifact missing artifact_type", label)
	}
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("%s artifact missing title", label)
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("%s artifact missing status", label)
	}
	if !isBrowserTraceAPIArtifactStatus(strings.TrimSpace(item.Status)) {
		return fmt.Errorf("%s artifact status invalid %q", label, item.Status)
	}
	if strings.TrimSpace(item.Content) == "" {
		return fmt.Errorf("%s artifact missing content", label)
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("%s artifact missing created_at", label)
	}
	return nil
}

func isBrowserTraceAPICandidateStatus(status string) bool {
	switch status {
	case "candidate":
		return true
	default:
		return false
	}
}

func isBrowserTraceAPIValidationStatus(status string) bool {
	switch status {
	case "validated", "needs_review":
		return true
	default:
		return false
	}
}

func isBrowserTraceAPIArtifactStatus(status string) bool {
	switch status {
	case "generated", "draft", "pending_review":
		return true
	default:
		return false
	}
}

func validateComplexityStatus(resp ComplexityStatus) error {
	seenScans := map[string]struct{}{}
	for _, scan := range resp.Scans {
		scanID := strings.TrimSpace(scan.ScanID)
		if scanID == "" {
			return fmt.Errorf("complexity status scan missing scan_id")
		}
		if strings.TrimSpace(scan.Repo) == "" {
			return fmt.Errorf("complexity status scan %s missing repo", scanID)
		}
		if strings.TrimSpace(scan.Mode) == "" {
			return fmt.Errorf("complexity status scan %s missing mode", scanID)
		}
		if strings.TrimSpace(scan.Status) == "" {
			return fmt.Errorf("complexity status scan %s missing status", scanID)
		}
		if scan.FilesScanned < 0 || scan.HotspotsFound < 0 {
			return fmt.Errorf("complexity status scan %s counts must be >= 0", scanID)
		}
		if scan.CreatedAt.IsZero() {
			return fmt.Errorf("complexity status scan %s missing created_at", scanID)
		}
		if _, ok := seenScans[scanID]; ok {
			return fmt.Errorf("complexity status contains duplicate scan for scan_id %q", scanID)
		}
		if strings.TrimSpace(scan.Status) == "completed" && scan.CompletedAt.IsZero() {
			return fmt.Errorf("complexity status completed scan %s missing completed_at", scanID)
		}
		seenScans[scanID] = struct{}{}
	}
	seenHotspots := map[string]struct{}{}
	for _, hotspot := range resp.Hotspots {
		hotspotID := strings.TrimSpace(hotspot.HotspotID)
		if hotspotID == "" {
			return fmt.Errorf("complexity status hotspot missing hotspot_id")
		}
		if strings.TrimSpace(hotspot.ScanID) == "" {
			return fmt.Errorf("complexity status hotspot missing scan_id")
		}
		if strings.TrimSpace(hotspot.FilePath) == "" {
			return fmt.Errorf("complexity status hotspot missing file_path")
		}
		if strings.TrimSpace(hotspot.HotspotType) == "" {
			return fmt.Errorf("complexity status hotspot %s missing hotspot_type", hotspotID)
		}
		if strings.TrimSpace(hotspot.RiskLevel) == "" {
			return fmt.Errorf("complexity status hotspot %s missing risk_level", hotspotID)
		}
		if strings.TrimSpace(hotspot.Summary) == "" {
			return fmt.Errorf("complexity status hotspot %s missing summary", hotspotID)
		}
		if err := validateLineRange(hotspot.LineStart, hotspot.LineEnd, fmt.Sprintf("complexity status hotspot %s", hotspotID)); err != nil {
			return err
		}
		if hotspot.Confidence < 0 || hotspot.Confidence > 1 {
			return fmt.Errorf("complexity status hotspot %s confidence out of range", hotspotID)
		}
		if hotspot.CreatedAt.IsZero() {
			return fmt.Errorf("complexity status hotspot %s missing created_at", hotspotID)
		}
		if _, ok := seenHotspots[hotspotID]; ok {
			return fmt.Errorf("complexity status contains duplicate hotspot for hotspot_id %q", hotspotID)
		}
		seenHotspots[hotspotID] = struct{}{}
	}
	seenEvidence := map[string]struct{}{}
	for _, evidence := range resp.Evidence {
		evidenceID := strings.TrimSpace(evidence.EvidenceID)
		if evidenceID == "" {
			return fmt.Errorf("complexity status evidence missing evidence_id")
		}
		if strings.TrimSpace(evidence.HotspotID) == "" {
			return fmt.Errorf("complexity status evidence missing hotspot_id")
		}
		if strings.TrimSpace(evidence.FilePath) == "" {
			return fmt.Errorf("complexity status evidence missing file_path")
		}
		if strings.TrimSpace(evidence.Reason) == "" {
			return fmt.Errorf("complexity status evidence %s missing reason", evidenceID)
		}
		if err := validateLineRange(evidence.LineStart, evidence.LineEnd, fmt.Sprintf("complexity status evidence %s", evidenceID)); err != nil {
			return err
		}
		if evidence.CreatedAt.IsZero() {
			return fmt.Errorf("complexity status evidence %s missing created_at", evidenceID)
		}
		if _, ok := seenEvidence[evidenceID]; ok {
			return fmt.Errorf("complexity status contains duplicate evidence for evidence_id %q", evidenceID)
		}
		seenEvidence[evidenceID] = struct{}{}
	}
	seenReports := map[string]struct{}{}
	for _, report := range resp.Reports {
		artifactID := strings.TrimSpace(report.ArtifactID)
		if artifactID == "" {
			return fmt.Errorf("complexity status report missing artifact_id")
		}
		if strings.TrimSpace(report.ScanID) == "" {
			return fmt.Errorf("complexity status report missing scan_id")
		}
		if strings.TrimSpace(report.Type) == "" {
			return fmt.Errorf("complexity status report %s missing artifact_type", artifactID)
		}
		if strings.TrimSpace(report.Title) == "" {
			return fmt.Errorf("complexity status report %s missing title", artifactID)
		}
		if strings.TrimSpace(report.Status) == "" {
			return fmt.Errorf("complexity status report %s missing status", artifactID)
		}
		if strings.TrimSpace(report.Content) == "" {
			return fmt.Errorf("complexity status report %s missing content", artifactID)
		}
		if err := validateComplexityReportReviewBoundary(report); err != nil {
			return err
		}
		if report.CreatedAt.IsZero() {
			return fmt.Errorf("complexity status report %s missing created_at", artifactID)
		}
		if _, ok := seenReports[artifactID]; ok {
			return fmt.Errorf("complexity status contains duplicate report for artifact_id %q", artifactID)
		}
		seenReports[artifactID] = struct{}{}
	}
	return nil
}

func validateComplexityReportReviewBoundary(report ComplexityReportArtifact) error {
	artifactID := strings.TrimSpace(report.ArtifactID)
	reportType := strings.TrimSpace(report.Type)
	status := strings.TrimSpace(report.Status)
	content := report.Content
	switch reportType {
	case "complexity_patch_proposal", "complexity_coder_diff_request", "complexity_concrete_diff_proposal":
		if status != "pending_review" {
			return fmt.Errorf("complexity status report %s review artifact status must be pending_review", artifactID)
		}
	case "complexity_hotspot_report":
		if status != "generated" && status != "pending_review" {
			return fmt.Errorf("complexity status report %s report artifact status must be generated or pending_review", artifactID)
		}
	case "complexity_coder_diff_failure":
		if status != "failed" {
			return fmt.Errorf("complexity status report %s coder diff failure status must be failed", artifactID)
		}
		if !strings.Contains(content, "Failure reason:") {
			return fmt.Errorf("complexity status report %s coder diff failure missing failure reason", artifactID)
		}
		if !strings.Contains(content, "Patch applied: `false`") {
			return fmt.Errorf("complexity status report %s coder diff failure must not claim patch applied", artifactID)
		}
	}
	if reportType == "complexity_concrete_diff_proposal" {
		if !strings.Contains(content, "Patch applied: `false`") {
			return fmt.Errorf("complexity status report %s concrete diff must not claim patch applied", artifactID)
		}
		if !strings.Contains(content, "Human approval required: `true`") {
			return fmt.Errorf("complexity status report %s concrete diff missing human approval requirement", artifactID)
		}
	}
	return nil
}

func validateLineRange(lineStart, lineEnd int, label string) error {
	if lineStart < 0 || lineEnd < 0 {
		return fmt.Errorf("%s line range must be >= 0", label)
	}
	if lineStart > 0 && lineEnd > 0 && lineEnd < lineStart {
		return fmt.Errorf("%s line_end must be >= line_start", label)
	}
	return nil
}

func validateAgentRunRequest(item AgentRun) error {
	if strings.TrimSpace(item.RunID) == "" {
		return fmt.Errorf("agent run request missing run_id")
	}
	if strings.TrimSpace(item.AgentType) == "" {
		return fmt.Errorf("agent run request missing agent_type")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("agent run request missing status")
	}
	return nil
}

func validateTraceEventRequest(item TraceEvent) error {
	if strings.TrimSpace(item.EventID) == "" {
		return fmt.Errorf("trace event request missing event_id")
	}
	if strings.TrimSpace(item.EventType) == "" {
		return fmt.Errorf("trace event request missing event_type")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("trace event request missing status")
	}
	return nil
}

func normalizeRunQueueCreateRequest(item RunQueueItem) RunQueueItem {
	if strings.TrimSpace(item.Status) == "" {
		item.Status = "queued"
	}
	return item
}

func validateRunQueueCreateRequest(item RunQueueItem) error {
	if strings.TrimSpace(item.QueueID) == "" {
		return fmt.Errorf("run queue create request missing queue_id")
	}
	if strings.TrimSpace(item.Goal) == "" {
		return fmt.Errorf("run queue create request missing goal")
	}
	if strings.TrimSpace(item.Action) == "" {
		return fmt.Errorf("run queue create request missing action")
	}
	if strings.TrimSpace(item.Status) != "queued" {
		return fmt.Errorf("run queue create request status must be queued")
	}
	return nil
}

func normalizeRunQueueCompleteRequest(req RunQueueCompleteRequest) RunQueueCompleteRequest {
	if strings.TrimSpace(req.Status) == "" {
		req.Status = "completed"
	}
	return req
}

func validateRunQueueCompleteRequest(req RunQueueCompleteRequest) error {
	if strings.TrimSpace(req.QueueID) == "" {
		return fmt.Errorf("run queue complete request missing queue_id")
	}
	switch strings.TrimSpace(req.Status) {
	case "completed", "failed", "cancelled":
		return nil
	default:
		return fmt.Errorf("run queue complete request status must be completed, failed, or cancelled")
	}
}

func validateRunQueueClaimResponse(resp RunQueueClaimResponse) error {
	if !resp.Claimed {
		if strings.TrimSpace(resp.Item.QueueID) != "" || strings.TrimSpace(resp.Item.Status) == "claimed" {
			return fmt.Errorf("run queue claim response is not claimed but includes claimed item state")
		}
		return nil
	}
	if strings.TrimSpace(resp.Item.QueueID) == "" {
		return fmt.Errorf("run queue claim response claimed without queue_id")
	}
	if strings.TrimSpace(resp.Item.Status) != "claimed" {
		return fmt.Errorf("run queue claim response status mismatch: got %q want %q", resp.Item.Status, "claimed")
	}
	if resp.Item.CreatedAt.IsZero() {
		return fmt.Errorf("run queue claim response item missing created_at")
	}
	return nil
}

func validateRunStateResponse(resp RunStateResponse, runID string, expectedStatus string, allowedActions map[string]bool) error {
	if resp.RunID != strings.TrimSpace(runID) {
		return fmt.Errorf("run state response run_id mismatch")
	}
	if strings.TrimSpace(resp.Status) != expectedStatus {
		return fmt.Errorf("run state response status mismatch: got %q want %q", resp.Status, expectedStatus)
	}
	if strings.TrimSpace(resp.EventID) == "" {
		return fmt.Errorf("run state response missing event_id")
	}
	action := strings.TrimSpace(resp.RuntimeControlAction)
	if !allowedActions[action] {
		return fmt.Errorf("run state response runtime_control_action mismatch: got %q", resp.RuntimeControlAction)
	}
	if resp.RuntimeControlApplied && action == "none" {
		return fmt.Errorf("run state response claims runtime control applied with action none")
	}
	return nil
}

func validateExternalControlRequest(req ExternalControlRequest) error {
	if strings.TrimSpace(req.Actor) == "" {
		return fmt.Errorf("external control request missing actor")
	}
	if strings.TrimSpace(req.ChannelID) == "" {
		return fmt.Errorf("external control request missing channel_id")
	}
	if strings.TrimSpace(req.Action) == "" {
		return fmt.Errorf("external control request missing action")
	}
	return nil
}

func validateExternalControlResponse(resp ExternalControlResponse, req ExternalControlRequest) error {
	if strings.TrimSpace(resp.Request.Actor) != strings.TrimSpace(req.Actor) ||
		strings.TrimSpace(resp.Request.ChannelID) != strings.TrimSpace(req.ChannelID) ||
		strings.TrimSpace(resp.Request.Action) != strings.TrimSpace(req.Action) ||
		resp.Request.HumanApproved != req.HumanApproved {
		return fmt.Errorf("external control response request mismatch")
	}
	status := strings.TrimSpace(resp.Decision.Status)
	switch status {
	case "allowed":
		if resp.Decision.RequiresApproval && !req.HumanApproved {
			return fmt.Errorf("external control response allowed approval-required action without human approval")
		}
	case "needs_approval":
		if !resp.Decision.RequiresApproval {
			return fmt.Errorf("external control response needs_approval without requires_approval")
		}
	case "blocked":
		if len(resp.Decision.Reasons) == 0 {
			return fmt.Errorf("external control response blocked without reasons")
		}
	default:
		return fmt.Errorf("external control response invalid status: %q", resp.Decision.Status)
	}
	return nil
}

func validateHeavyWorkerRequest(req HeavyWorkerRequest) error {
	if strings.TrimSpace(req.Agent) == "" {
		return fmt.Errorf("heavy worker request missing agent")
	}
	if req.TargetFileCount < 0 || req.RelatedSpecCount < 0 || req.FailedAttempts < 0 {
		return fmt.Errorf("heavy worker request counts must be >= 0")
	}
	return nil
}

func validateHeavyWorkerResponse(resp HeavyWorkerResponse, req HeavyWorkerRequest) error {
	if strings.TrimSpace(resp.Request.EventID) != strings.TrimSpace(req.EventID) ||
		strings.TrimSpace(resp.Request.Agent) != strings.TrimSpace(req.Agent) ||
		resp.Request.TargetFileCount != req.TargetFileCount ||
		resp.Request.RelatedSpecCount != req.RelatedSpecCount ||
		resp.Request.CrossesArchitectureBoundary != req.CrossesArchitectureBoundary ||
		resp.Request.HighUncertainty != req.HighUncertainty ||
		resp.Request.FailedAttempts != req.FailedAttempts ||
		resp.Request.UserRequestedDeepDive != req.UserRequestedDeepDive ||
		strings.TrimSpace(resp.Request.Reason) != strings.TrimSpace(req.Reason) {
		return fmt.Errorf("heavy worker response request mismatch")
	}
	status := strings.TrimSpace(resp.Decision.Status)
	switch status {
	case "not_required", "blocked":
		if len(resp.Decision.Reasons) == 0 {
			return fmt.Errorf("heavy worker response %s without reasons", status)
		}
		if resp.Event != nil {
			return fmt.Errorf("heavy worker response %s should not include event", status)
		}
	case "requested":
		if len(resp.Decision.Reasons) == 0 {
			return fmt.Errorf("heavy worker response requested without reasons")
		}
		if resp.Event == nil {
			return fmt.Errorf("heavy worker response requested missing event")
		}
		if strings.TrimSpace(resp.Event.EventType) != "heavy_worker_requested" {
			return fmt.Errorf("heavy worker response event_type mismatch")
		}
		if strings.TrimSpace(resp.Event.Status) != "requested" {
			return fmt.Errorf("heavy worker response event status mismatch")
		}
		if strings.TrimSpace(resp.Event.Agent) != strings.TrimSpace(req.Agent) {
			return fmt.Errorf("heavy worker response event agent mismatch")
		}
		if strings.TrimSpace(req.EventID) != "" && strings.TrimSpace(resp.Event.ParentEventID) != strings.TrimSpace(req.EventID) {
			return fmt.Errorf("heavy worker response event parent mismatch")
		}
		if resp.Event.CreatedAt.IsZero() {
			return fmt.Errorf("heavy worker response event missing created_at")
		}
	default:
		return fmt.Errorf("heavy worker response invalid status: %q", resp.Decision.Status)
	}
	return nil
}

func validateContextBudgetPolicy(policy ContextBudgetPolicy) error {
	if policy.MaxContextTokens < 0 {
		return fmt.Errorf("ai workflow status context budget policy has negative max_context_tokens")
	}
	if policy.MaxContextTokens == 0 {
		return nil
	}
	if policy.WarnAtRatio <= 0 || policy.WarnAtRatio >= 1 {
		return fmt.Errorf("ai workflow status context budget policy has invalid warn_at_ratio %.3f", policy.WarnAtRatio)
	}
	if policy.StopAtRatio <= 0 || policy.StopAtRatio > 1 {
		return fmt.Errorf("ai workflow status context budget policy has invalid stop_at_ratio %.3f", policy.StopAtRatio)
	}
	if policy.WarnAtRatio >= policy.StopAtRatio {
		return fmt.Errorf("ai workflow status context budget policy warn_at_ratio must be less than stop_at_ratio")
	}
	return nil
}

func validateHeavyWorkerRuntimeDiagnostics(resp HeavyWorkerRuntimeDiagnostics) error {
	if strings.TrimSpace(resp.Role) != "Heavy" {
		return fmt.Errorf("heavy worker diagnostics role mismatch: %q", resp.Role)
	}
	if strings.TrimSpace(resp.Route) != "ANALYZE" {
		return fmt.Errorf("heavy worker diagnostics route mismatch: %q", resp.Route)
	}
	if strings.TrimSpace(resp.RoutePrefix) != "/analyze" {
		return fmt.Errorf("heavy worker diagnostics route_prefix mismatch: %q", resp.RoutePrefix)
	}
	if !resp.FailureIsError {
		return fmt.Errorf("heavy worker diagnostics must mark failure_is_error")
	}
	if resp.Configured {
		if strings.TrimSpace(resp.BaseURL) == "" || strings.TrimSpace(resp.Model) == "" {
			return fmt.Errorf("heavy worker diagnostics configured without base_url/model")
		}
	}
	if resp.LLMOps.Configured && resp.LLMOps.Enabled && !resp.LLMOps.LiveAvailable && strings.TrimSpace(resp.LLMOps.Error) == "" {
		return fmt.Errorf("heavy worker diagnostics llm_ops unavailable without error")
	}
	if resp.LLMOps.LiveAvailable && !resp.LLMOps.Enabled {
		return fmt.Errorf("heavy worker diagnostics llm_ops live while disabled")
	}
	return nil
}

func validateCommandRunRequest(req CommandRunRequest) error {
	if strings.TrimSpace(req.CommandName) == "" {
		return fmt.Errorf("command run request missing command_name")
	}
	return nil
}

func validateCommandRunResponse(resp CommandRunResponse, req CommandRunRequest) error {
	requested := strings.TrimSpace(req.CommandName)
	if requested == "" {
		return fmt.Errorf("command run request command_name is required")
	}
	if strings.TrimSpace(resp.Command.CommandName) != requested {
		return fmt.Errorf("command run response command_name mismatch")
	}
	if strings.TrimSpace(resp.Event.EventID) == "" {
		return fmt.Errorf("command run response missing event_id")
	}
	if strings.TrimSpace(resp.Event.EventType) != "command_invoked" {
		return fmt.Errorf("command run response event_type mismatch: got %q", resp.Event.EventType)
	}
	if strings.TrimSpace(resp.Event.CommandName) != requested {
		return fmt.Errorf("command run response event command_name mismatch")
	}
	if strings.TrimSpace(resp.Event.Status) != "requested" {
		return fmt.Errorf("command run response status mismatch: got %q", resp.Event.Status)
	}
	if resp.Event.CreatedAt.IsZero() {
		return fmt.Errorf("command run response event missing created_at")
	}
	if strings.TrimSpace(req.RunID) != "" && strings.TrimSpace(resp.Event.RunID) != strings.TrimSpace(req.RunID) {
		return fmt.Errorf("command run response run_id mismatch")
	}
	if strings.TrimSpace(req.WorkstreamID) != "" && strings.TrimSpace(resp.Event.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
		return fmt.Errorf("command run response workstream_id mismatch")
	}
	return nil
}

func validateContextUsageRequest(usage ContextUsage) error {
	if strings.TrimSpace(usage.EventID) == "" {
		return fmt.Errorf("context budget request missing event_id")
	}
	if strings.TrimSpace(usage.Agent) == "" {
		return fmt.Errorf("context budget request missing agent")
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.ContextTokens < 0 ||
		usage.ToolCallCount < 0 || usage.DCICallCount < 0 || usage.RepairCount < 0 || usage.LatencyMS < 0 {
		return fmt.Errorf("context budget request counts must be >= 0")
	}
	return nil
}

func validateContextBudgetResponse(resp ContextBudgetResponse, req ContextUsage) error {
	if strings.TrimSpace(req.EventID) != "" && strings.TrimSpace(resp.ContextUsage.EventID) != strings.TrimSpace(req.EventID) {
		return fmt.Errorf("context budget response event_id mismatch")
	}
	if strings.TrimSpace(req.Agent) != "" && strings.TrimSpace(resp.ContextUsage.Agent) != strings.TrimSpace(req.Agent) {
		return fmt.Errorf("context budget response agent mismatch")
	}
	if resp.ContextUsage.ContextTokens != req.ContextTokens {
		return fmt.Errorf("context budget response context_tokens mismatch")
	}
	if resp.ContextUsage.CreatedAt.IsZero() {
		return fmt.Errorf("context budget response context_usage missing created_at")
	}
	status := strings.TrimSpace(resp.Decision.Status)
	switch status {
	case "ok":
		if resp.Event != nil {
			return fmt.Errorf("context budget response ok should not include budget event")
		}
	case "warn", "stop":
		if resp.Event == nil {
			return fmt.Errorf("context budget response %s missing budget event", status)
		}
		if strings.TrimSpace(resp.Event.ParentEventID) != strings.TrimSpace(resp.ContextUsage.EventID) {
			return fmt.Errorf("context budget response event parent mismatch")
		}
		if strings.TrimSpace(resp.Event.Status) != status {
			return fmt.Errorf("context budget response event status mismatch")
		}
		wantType := "context_budget_warning"
		if status == "stop" {
			wantType = "context_budget_exceeded"
		}
		if strings.TrimSpace(resp.Event.EventType) != wantType {
			return fmt.Errorf("context budget response event_type mismatch: got %q want %q", resp.Event.EventType, wantType)
		}
		if resp.Event.CreatedAt.IsZero() {
			return fmt.Errorf("context budget response event missing created_at")
		}
	default:
		return fmt.Errorf("context budget response invalid status: %q", resp.Decision.Status)
	}
	if resp.Decision.ContextTokens != resp.ContextUsage.ContextTokens {
		return fmt.Errorf("context budget response decision context_tokens mismatch")
	}
	return nil
}

func validateWorkstreamStatus(resp WorkstreamStatus) error {
	workstreams := map[string]struct{}{}
	for _, item := range resp.Workstreams {
		id := strings.TrimSpace(item.WorkstreamID)
		if id == "" {
			return fmt.Errorf("workstream status workstreams missing workstream_id")
		}
		if _, ok := workstreams[id]; ok {
			return fmt.Errorf("workstream status duplicate workstream workstream_id=%s", id)
		}
		workstreams[id] = struct{}{}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("workstream status workstream missing name")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status workstream missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status workstream %s missing created_at", id)
		}
	}
	goals := map[string]struct{}{}
	for _, item := range resp.Goals {
		id := strings.TrimSpace(item.GoalID)
		if id == "" {
			return fmt.Errorf("workstream status goals missing goal_id")
		}
		if _, ok := goals[id]; ok {
			return fmt.Errorf("workstream status duplicate goal goal_id=%s", id)
		}
		goals[id] = struct{}{}
		if strings.TrimSpace(item.WorkstreamID) == "" {
			return fmt.Errorf("workstream status goal missing workstream_id")
		}
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("workstream status goal missing title")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status goal missing status")
		}
		if strings.TrimSpace(item.Status) == "completed" && item.CompletedAt.IsZero() {
			return fmt.Errorf("workstream status completed goal %s missing completed_at", id)
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status goal %s missing created_at", id)
		}
	}
	artifacts := map[string]struct{}{}
	for _, item := range resp.Artifacts {
		id := strings.TrimSpace(item.ArtifactID)
		if id == "" {
			return fmt.Errorf("workstream status artifacts missing artifact_id")
		}
		if _, ok := artifacts[id]; ok {
			return fmt.Errorf("workstream status duplicate artifact artifact_id=%s", id)
		}
		artifacts[id] = struct{}{}
		if strings.TrimSpace(item.WorkstreamID) == "" {
			return fmt.Errorf("workstream status artifact missing workstream_id")
		}
		if strings.TrimSpace(item.Type) == "" {
			return fmt.Errorf("workstream status artifact missing artifact_type")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status artifact missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status artifact %s missing created_at", id)
		}
	}
	annotations := map[string]struct{}{}
	for _, item := range resp.Annotations {
		id := strings.TrimSpace(item.AnnotationID)
		if id == "" {
			return fmt.Errorf("workstream status annotations missing annotation_id")
		}
		if _, ok := annotations[id]; ok {
			return fmt.Errorf("workstream status duplicate annotation annotation_id=%s", id)
		}
		annotations[id] = struct{}{}
		if strings.TrimSpace(item.ArtifactID) == "" {
			return fmt.Errorf("workstream status annotation missing artifact_id")
		}
		if strings.TrimSpace(item.Comment) == "" {
			return fmt.Errorf("workstream status annotation missing comment")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status annotation missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status annotation %s missing created_at", id)
		}
	}
	steering := map[string]struct{}{}
	for _, item := range resp.Steering {
		id := strings.TrimSpace(item.SteeringID)
		if id == "" {
			return fmt.Errorf("workstream status steering missing steering_id")
		}
		if _, ok := steering[id]; ok {
			return fmt.Errorf("workstream status duplicate steering steering_id=%s", id)
		}
		steering[id] = struct{}{}
		if strings.TrimSpace(item.WorkstreamID) == "" {
			return fmt.Errorf("workstream status steering missing workstream_id")
		}
		if strings.TrimSpace(item.Instruction) == "" {
			return fmt.Errorf("workstream status steering missing instruction")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status steering missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status steering %s missing created_at", id)
		}
	}
	heartbeats := map[string]struct{}{}
	for _, item := range resp.Heartbeats {
		id := strings.TrimSpace(item.HeartbeatID)
		if id == "" {
			return fmt.Errorf("workstream status heartbeats missing heartbeat_id")
		}
		if _, ok := heartbeats[id]; ok {
			return fmt.Errorf("workstream status duplicate heartbeat heartbeat_id=%s", id)
		}
		heartbeats[id] = struct{}{}
		if strings.TrimSpace(item.WorkstreamID) == "" {
			return fmt.Errorf("workstream status heartbeat missing workstream_id")
		}
		if strings.TrimSpace(item.ScheduleText) == "" {
			return fmt.Errorf("workstream status heartbeat missing schedule_text")
		}
		if strings.TrimSpace(item.Task) == "" {
			return fmt.Errorf("workstream status heartbeat missing task")
		}
		if strings.TrimSpace(item.Status) == "" {
			return fmt.Errorf("workstream status heartbeat missing status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status heartbeat %s missing created_at", id)
		}
	}
	vaultUpdates := map[string]struct{}{}
	for _, item := range resp.VaultUpdates {
		id := strings.TrimSpace(item.UpdateID)
		if id == "" {
			return fmt.Errorf("workstream status vault_updates missing update_id")
		}
		if _, ok := vaultUpdates[id]; ok {
			return fmt.Errorf("workstream status duplicate vault_update update_id=%s", id)
		}
		vaultUpdates[id] = struct{}{}
		if strings.TrimSpace(item.WorkstreamID) == "" {
			return fmt.Errorf("workstream status vault_update missing workstream_id")
		}
		if strings.TrimSpace(item.FilePath) == "" {
			return fmt.Errorf("workstream status vault_update missing file_path")
		}
		if strings.TrimSpace(item.ReviewStatus) == "" {
			return fmt.Errorf("workstream status vault_update missing review_status")
		}
		if item.CreatedAt.IsZero() {
			return fmt.Errorf("workstream status vault_update %s missing created_at", id)
		}
		switch strings.TrimSpace(item.ReviewStatus) {
		case "pending", "approved", "rejected":
		default:
			return fmt.Errorf("workstream status invalid vault_update review_status=%q", item.ReviewStatus)
		}
		if item.Applied && strings.TrimSpace(item.ReviewStatus) != "approved" {
			return fmt.Errorf("workstream status vault_update applied without approved review")
		}
		if item.Applied && strings.TrimSpace(item.AppliedPath) == "" {
			return fmt.Errorf("workstream status vault_update applied without applied_path")
		}
		if !item.Applied && strings.TrimSpace(item.AppliedPath) != "" {
			return fmt.Errorf("workstream status vault_update has applied_path without applied")
		}
	}
	return nil
}

func validateWorkstreamArtifactResponse(resp WorkstreamArtifactResponse, req WorkstreamArtifact) error {
	if strings.TrimSpace(resp.Artifact.ArtifactID) != strings.TrimSpace(req.ArtifactID) {
		return fmt.Errorf("workstream artifact response artifact_id mismatch")
	}
	if strings.TrimSpace(resp.Artifact.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
		return fmt.Errorf("workstream artifact response workstream_id mismatch")
	}
	if strings.TrimSpace(resp.Artifact.Type) != strings.TrimSpace(req.Type) {
		return fmt.Errorf("workstream artifact response artifact_type mismatch")
	}
	expectedStatus := strings.TrimSpace(req.Status)
	if expectedStatus == "" {
		expectedStatus = "draft"
	}
	if strings.TrimSpace(resp.Artifact.Status) != expectedStatus {
		return fmt.Errorf("workstream artifact response status mismatch: got %q want %q", resp.Artifact.Status, expectedStatus)
	}
	if resp.Artifact.CreatedAt.IsZero() {
		return fmt.Errorf("workstream artifact response artifact missing created_at")
	}
	return nil
}

func validateWorkstreamArtifactRequest(item WorkstreamArtifact) error {
	if strings.TrimSpace(item.ArtifactID) == "" {
		return fmt.Errorf("workstream artifact request missing artifact_id")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return fmt.Errorf("workstream artifact request missing workstream_id")
	}
	if strings.TrimSpace(item.Type) == "" {
		return fmt.Errorf("workstream artifact request missing artifact_type")
	}
	return nil
}

func validateWorkstreamVaultUpdateRequest(item WorkstreamVaultUpdate) error {
	if strings.TrimSpace(item.UpdateID) == "" {
		return fmt.Errorf("workstream vault update request missing update_id")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return fmt.Errorf("workstream vault update request missing workstream_id")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return fmt.Errorf("workstream vault update request missing file_path")
	}
	status := strings.TrimSpace(item.ReviewStatus)
	if status == "" {
		return fmt.Errorf("workstream vault update request missing review_status")
	}
	switch status {
	case "pending", "approved", "rejected":
	default:
		return fmt.Errorf("workstream vault update request invalid review_status=%q", status)
	}
	return nil
}

func validateWorkstreamVaultUpdateCreateResponse(resp WorkstreamVaultUpdateResponse, req WorkstreamVaultUpdate) error {
	if err := validateWorkstreamVaultUpdateEcho(resp.VaultUpdate, req); err != nil {
		return err
	}
	if resp.Applied {
		return fmt.Errorf("workstream vault update create response must not apply changes")
	}
	if strings.TrimSpace(resp.AppliedPath) != "" {
		return fmt.Errorf("workstream vault update create response has applied_path without apply")
	}
	if resp.VaultUpdate.CreatedAt.IsZero() {
		return fmt.Errorf("workstream vault update response vault_update missing created_at")
	}
	return nil
}

func validateWorkstreamVaultUpdatePreviewResponse(resp WorkstreamVaultUpdatePreviewResponse, req WorkstreamVaultUpdate) error {
	if strings.TrimSpace(resp.Preview.UpdateID) != strings.TrimSpace(req.UpdateID) {
		return fmt.Errorf("workstream vault preview response update_id mismatch")
	}
	if strings.TrimSpace(resp.Preview.FilePath) != strings.TrimSpace(req.FilePath) {
		return fmt.Errorf("workstream vault preview response file_path mismatch")
	}
	if strings.TrimSpace(req.ProposedContent) != "" && strings.TrimSpace(resp.Preview.ProposedContent) == "" {
		return fmt.Errorf("workstream vault preview response missing proposed_content")
	}
	if strings.TrimSpace(req.ProposedContent) != "" && strings.TrimSpace(resp.Preview.UnifiedDiff) == "" {
		return fmt.Errorf("workstream vault preview response missing unified_diff")
	}
	if resp.Preview.AddedLines < 0 || resp.Preview.RemovedLines < 0 {
		return fmt.Errorf("workstream vault preview response invalid line counts")
	}
	return nil
}

func validateWorkstreamVaultReviewRequest(item WorkstreamVaultUpdate) error {
	if strings.TrimSpace(item.UpdateID) == "" {
		return fmt.Errorf("workstream vault review request missing update_id")
	}
	if strings.TrimSpace(item.WorkstreamID) == "" {
		return fmt.Errorf("workstream vault review request missing workstream_id")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return fmt.Errorf("workstream vault review request missing file_path")
	}
	if strings.TrimSpace(item.ReviewStatus) == "" {
		return fmt.Errorf("workstream vault review request missing review_status")
	}
	return nil
}

func validateWorkstreamVaultUpdateEcho(got WorkstreamVaultUpdate, req WorkstreamVaultUpdate) error {
	if strings.TrimSpace(got.UpdateID) != strings.TrimSpace(req.UpdateID) {
		return fmt.Errorf("workstream vault update response update_id mismatch")
	}
	if strings.TrimSpace(got.WorkstreamID) != strings.TrimSpace(req.WorkstreamID) {
		return fmt.Errorf("workstream vault update response workstream_id mismatch")
	}
	if strings.TrimSpace(got.FilePath) != strings.TrimSpace(req.FilePath) {
		return fmt.Errorf("workstream vault update response file_path mismatch")
	}
	if strings.TrimSpace(got.ReviewStatus) != strings.TrimSpace(req.ReviewStatus) {
		return fmt.Errorf("workstream vault update response status mismatch: got %q want %q", got.ReviewStatus, req.ReviewStatus)
	}
	if got.Applied && strings.TrimSpace(got.AppliedPath) == "" {
		return fmt.Errorf("workstream vault update response applied without applied_path")
	}
	if !got.Applied && strings.TrimSpace(got.AppliedPath) != "" {
		return fmt.Errorf("workstream vault update response has applied_path without applied")
	}
	return nil
}

func validateWorkstreamVaultUpdateReviewResponse(resp WorkstreamVaultUpdateResponse, req WorkstreamVaultUpdate) error {
	status := strings.TrimSpace(req.ReviewStatus)
	switch status {
	case "approved", "rejected":
	default:
		return fmt.Errorf("workstream vault review request status must be approved or rejected")
	}
	if err := validateWorkstreamVaultUpdateEcho(resp.VaultUpdate, req); err != nil {
		return fmt.Errorf("%s", strings.NewReplacer("workstream vault update response", "workstream vault review response").Replace(err.Error()))
	}
	if resp.Applied && strings.TrimSpace(resp.AppliedPath) == "" {
		return fmt.Errorf("workstream vault review response applied without applied_path")
	}
	if !resp.Applied && strings.TrimSpace(resp.AppliedPath) != "" {
		return fmt.Errorf("workstream vault review response has applied_path without applied")
	}
	if resp.Applied != resp.VaultUpdate.Applied {
		return fmt.Errorf("workstream vault review response applied mismatch")
	}
	if strings.TrimSpace(resp.AppliedPath) != strings.TrimSpace(resp.VaultUpdate.AppliedPath) {
		return fmt.Errorf("workstream vault review response applied_path mismatch")
	}
	if status == "approved" && strings.TrimSpace(req.ProposedContent) != "" && !resp.Applied {
		return fmt.Errorf("workstream vault review response did not apply approved proposed_content")
	}
	if resp.VaultUpdate.CreatedAt.IsZero() {
		return fmt.Errorf("workstream vault review response vault_update missing created_at")
	}
	return nil
}

func validateRevenueHumanDecisionResponse(resp RevenueHumanDecisionResponse, req RevenueHumanDecision, expectedApprovalStatus string) error {
	if strings.TrimSpace(req.DecisionID) != "" && strings.TrimSpace(resp.Record.DecisionID) != strings.TrimSpace(req.DecisionID) {
		return fmt.Errorf("revenue human decision response decision_id mismatch")
	}
	if strings.TrimSpace(req.DecisionType) != "" && strings.TrimSpace(resp.Record.DecisionType) != strings.TrimSpace(req.DecisionType) {
		return fmt.Errorf("revenue human decision response decision_type mismatch")
	}
	if strings.TrimSpace(resp.Record.DecisionID) == "" {
		return fmt.Errorf("revenue human decision response missing decision_id")
	}
	if strings.TrimSpace(resp.Record.DecisionType) == "" {
		return fmt.Errorf("revenue human decision response missing decision_type")
	}
	if strings.TrimSpace(resp.Result.Status) != strings.TrimSpace(resp.Record.GateStatus) {
		return fmt.Errorf("revenue human decision response status mismatch")
	}
	if resp.Result.RequiresApproval != resp.Record.RequiresApproval {
		return fmt.Errorf("revenue human decision response requires_approval mismatch")
	}
	expectedApprovalStatus = strings.TrimSpace(expectedApprovalStatus)
	if expectedApprovalStatus != "" && strings.TrimSpace(resp.Record.ApprovalStatus) != expectedApprovalStatus {
		return fmt.Errorf("revenue human decision response approval_status mismatch")
	}
	if strings.TrimSpace(resp.Record.GateStatus) == "blocked" && len(resp.Record.Reasons) == 0 && len(resp.Result.Reasons) == 0 {
		return fmt.Errorf("revenue human decision response blocked without reasons")
	}
	if strings.TrimSpace(resp.Record.GateStatus) == "needs_review" && !resp.Record.RequiresApproval {
		return fmt.Errorf("revenue human decision response needs_review without approval requirement")
	}
	if resp.Record.CreatedAt.IsZero() {
		return fmt.Errorf("revenue human decision response record missing created_at")
	}
	return nil
}

func validateSkillGovernanceContributionGateResponse(resp SkillGovernanceContributionGateResponse, req SkillGovernanceContributionGateRequest) error {
	if strings.TrimSpace(req.EventID) != "" && strings.TrimSpace(resp.GateLog.EventID) != strings.TrimSpace(req.EventID) {
		return fmt.Errorf("skill contribution gate response event_id mismatch")
	}
	if strings.TrimSpace(resp.GateLog.EventID) == "" {
		return fmt.Errorf("skill contribution gate response missing event_id")
	}
	if strings.TrimSpace(resp.GateLog.Repo) != strings.TrimSpace(req.Repo) {
		return fmt.Errorf("skill contribution gate response repo mismatch")
	}
	if strings.TrimSpace(resp.GateLog.GateStatus) != strings.TrimSpace(resp.Decision.Status) {
		return fmt.Errorf("skill contribution gate response status mismatch")
	}
	switch strings.TrimSpace(resp.Decision.Status) {
	case "passed":
		if !resp.Decision.CanContribute {
			return fmt.Errorf("skill contribution gate response passed without can_contribute")
		}
		if len(resp.Decision.StopReasons) > 0 {
			return fmt.Errorf("skill contribution gate response passed with stop reasons")
		}
	case "blocked":
		if resp.Decision.CanContribute {
			return fmt.Errorf("skill contribution gate response blocked with can_contribute")
		}
		if len(resp.Decision.StopReasons) == 0 {
			return fmt.Errorf("skill contribution gate response blocked without stop reasons")
		}
	default:
		return fmt.Errorf("skill contribution gate response invalid status: %q", resp.Decision.Status)
	}
	if resp.GateLog.CreatedAt.IsZero() {
		return fmt.Errorf("skill contribution gate response gate log missing created_at")
	}
	return nil
}

func (c *Client) EvaluateSkillGovernanceContributionGate(ctx context.Context, item SkillGovernanceContributionGateRequest) (SkillGovernanceContributionGateResponse, error) {
	if err := validateSkillGovernanceContributionGateRequest(item); err != nil {
		return SkillGovernanceContributionGateResponse{}, err
	}
	var out SkillGovernanceContributionGateResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/skill-governance/contribution-gate", item, &out); err != nil {
		return SkillGovernanceContributionGateResponse{}, err
	}
	if err := validateSkillGovernanceContributionGateResponse(out, item); err != nil {
		return SkillGovernanceContributionGateResponse{}, err
	}
	return out, nil
}

func (c *Client) SandboxStatus(ctx context.Context, limit int) (SandboxStatus, error) {
	path := "/viewer/sandbox"
	if limit > 0 {
		path = fmt.Sprintf("%s?limit=%d", path, limit)
	}
	var out SandboxStatus
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return SandboxStatus{}, err
	}
	if err := validateSandboxStatus(out); err != nil {
		return SandboxStatus{}, err
	}
	return out, nil
}

func validateSandboxStatus(resp SandboxStatus) error {
	seenSandboxes := map[string]struct{}{}
	for _, sandbox := range resp.Sandboxes {
		sandboxID := strings.TrimSpace(sandbox.SandboxID)
		if sandboxID == "" {
			return fmt.Errorf("sandbox status sandbox missing sandbox_id")
		}
		if strings.TrimSpace(sandbox.Type) == "" {
			return fmt.Errorf("sandbox status sandbox missing type")
		}
		if strings.TrimSpace(sandbox.Status) == "" {
			return fmt.Errorf("sandbox status sandbox missing status")
		}
		if sandbox.CreatedAt.IsZero() {
			return fmt.Errorf("sandbox status sandbox %s missing created_at", sandboxID)
		}
		if _, ok := seenSandboxes[sandboxID]; ok {
			return fmt.Errorf("sandbox status contains duplicate sandbox for sandbox_id %q", sandboxID)
		}
		seenSandboxes[sandboxID] = struct{}{}
	}
	seenArtifacts := map[string]struct{}{}
	completedArtifacts := map[string]struct{}{}
	for _, artifact := range resp.Artifacts {
		artifactID := strings.TrimSpace(artifact.ArtifactID)
		sandboxID := strings.TrimSpace(artifact.SandboxID)
		artifactType := strings.TrimSpace(artifact.Type)
		filePath := strings.TrimSpace(artifact.FilePath)
		if artifactID == "" {
			return fmt.Errorf("sandbox status artifact missing artifact_id")
		}
		if sandboxID == "" {
			return fmt.Errorf("sandbox status artifact missing sandbox_id")
		}
		if artifactType == "" {
			return fmt.Errorf("sandbox status artifact missing artifact_type")
		}
		if strings.TrimSpace(artifact.Status) == "" {
			return fmt.Errorf("sandbox status artifact missing status")
		}
		if artifact.CreatedAt.IsZero() {
			return fmt.Errorf("sandbox status artifact %s missing created_at", artifactID)
		}
		if _, ok := seenArtifacts[artifactID]; ok {
			return fmt.Errorf("sandbox status contains duplicate artifact for artifact_id %q", artifactID)
		}
		seenArtifacts[artifactID] = struct{}{}
		if strings.TrimSpace(artifact.Status) == "completed" && filePath != "" {
			completedArtifacts[sandboxArtifactKey(sandboxID, artifactType, filePath)] = struct{}{}
		}
	}
	seenPromotions := map[string]struct{}{}
	promotionsByID := map[string]PromotionRequest{}
	for _, promotion := range resp.Promotions {
		promotionID := strings.TrimSpace(promotion.PromotionID)
		if promotionID == "" {
			return fmt.Errorf("sandbox status promotion missing promotion_id")
		}
		if strings.TrimSpace(promotion.SandboxID) == "" {
			return fmt.Errorf("sandbox status promotion missing sandbox_id")
		}
		if strings.TrimSpace(promotion.TargetPath) == "" {
			return fmt.Errorf("sandbox status promotion missing target_path")
		}
		if promotion.CreatedAt.IsZero() {
			return fmt.Errorf("sandbox status promotion %s missing created_at", promotionID)
		}
		if _, ok := seenPromotions[promotionID]; ok {
			return fmt.Errorf("sandbox status contains duplicate promotion for promotion_id %q", promotionID)
		}
		seenPromotions[promotionID] = struct{}{}
		promotionsByID[promotionID] = promotion
	}
	for _, decision := range resp.Decisions {
		status := strings.TrimSpace(decision.Status)
		if status == "" {
			return fmt.Errorf("sandbox status decision missing status")
		}
		if !isSandboxGateStatus(status) {
			return fmt.Errorf("sandbox status invalid decision status=%q", decision.Status)
		}
	}
	seenGateLogs := map[string]struct{}{}
	for _, log := range resp.GateLogs {
		eventID := strings.TrimSpace(log.EventID)
		if eventID == "" {
			return fmt.Errorf("sandbox status gate_log missing event_id")
		}
		if strings.TrimSpace(log.PromotionID) == "" {
			return fmt.Errorf("sandbox status gate_log missing promotion_id")
		}
		if strings.TrimSpace(log.GateStatus) == "" {
			return fmt.Errorf("sandbox status gate_log missing gate_status")
		}
		status := strings.TrimSpace(log.GateStatus)
		if !isSandboxGateStatus(status) {
			return fmt.Errorf("sandbox status invalid gate_status=%q", log.GateStatus)
		}
		if log.CreatedAt.IsZero() {
			return fmt.Errorf("sandbox status gate_log %s missing created_at", eventID)
		}
		if _, ok := seenGateLogs[eventID]; ok {
			return fmt.Errorf("sandbox status contains duplicate gate_log for event_id %q", eventID)
		}
		seenGateLogs[eventID] = struct{}{}
		if status == "promotion_applied" || status == "rollback_executed" {
			if strings.TrimSpace(log.HumanApprovalStatus) != "granted" {
				return fmt.Errorf("sandbox status %s gate_log requires human approval", status)
			}
			promotion, ok := promotionsByID[strings.TrimSpace(log.PromotionID)]
			if !ok {
				return fmt.Errorf("sandbox status %s missing promotion record", status)
			}
			if strings.TrimSpace(promotion.HumanApprovalStatus) != "granted" {
				return fmt.Errorf("sandbox status %s promotion requires human approval", status)
			}
			switch status {
			case "promotion_applied":
				if strings.TrimSpace(promotion.DiffPath) == "" {
					return fmt.Errorf("sandbox status promotion_applied promotion missing diff_path")
				}
				postApplyPath := strings.TrimSpace(log.PostApplyVerification)
				if postApplyPath == "" {
					return fmt.Errorf("sandbox status promotion_applied gate_log missing post_apply_verification")
				}
				if strings.TrimSpace(promotion.PostApplyVerificationPath) == "" {
					return fmt.Errorf("sandbox status promotion_applied promotion missing post_apply_verification_path")
				}
				if postApplyPath != strings.TrimSpace(promotion.PostApplyVerificationPath) {
					return fmt.Errorf("sandbox status promotion_applied post_apply_verification mismatch")
				}
				if _, ok := completedArtifacts[sandboxArtifactKey(strings.TrimSpace(promotion.SandboxID), "post_apply_verification", postApplyPath)]; !ok {
					return fmt.Errorf("sandbox status promotion_applied missing completed post_apply_verification artifact")
				}
			case "rollback_executed":
				rollbackPlanPath := strings.TrimSpace(promotion.RollbackPlanPath)
				if rollbackPlanPath == "" {
					return fmt.Errorf("sandbox status rollback_executed promotion missing rollback_plan_path")
				}
				if _, ok := completedArtifacts[sandboxArtifactKey(strings.TrimSpace(promotion.SandboxID), "rollback_execution", rollbackPlanPath)]; !ok {
					return fmt.Errorf("sandbox status rollback_executed missing completed rollback_execution artifact")
				}
			}
		}
	}
	return nil
}

func sandboxArtifactKey(sandboxID, artifactType, filePath string) string {
	return strings.TrimSpace(sandboxID) + "\x00" + strings.TrimSpace(artifactType) + "\x00" + strings.TrimSpace(filePath)
}

func (c *Client) CreatePromotionRequest(ctx context.Context, req PromotionRequest) (PromotionRequestResponse, error) {
	if err := validatePromotionRequest(req); err != nil {
		return PromotionRequestResponse{}, err
	}
	var out PromotionRequestResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/sandbox/promotions", req, &out); err != nil {
		return PromotionRequestResponse{}, err
	}
	if err := validatePromotionRequestResponse(out, req); err != nil {
		return PromotionRequestResponse{}, err
	}
	return out, nil
}

func (c *Client) ApplyPromotion(ctx context.Context, req PromotionApplyRequest) (PromotionApplyResponse, error) {
	if err := validatePromotionApplyRequest(req); err != nil {
		return PromotionApplyResponse{}, err
	}
	out, err := c.applyPromotionUnchecked(ctx, req)
	if err != nil {
		return PromotionApplyResponse{}, err
	}
	if err := validatePromotionApplyResponse(out, req); err != nil {
		return PromotionApplyResponse{}, err
	}
	return out, nil
}

func (c *Client) applyPromotionUnchecked(ctx context.Context, req PromotionApplyRequest) (PromotionApplyResponse, error) {
	var out PromotionApplyResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/sandbox/promotions/apply", req, &out); err != nil {
		return PromotionApplyResponse{}, err
	}
	return out, nil
}

func (c *Client) RollbackPromotion(ctx context.Context, req PromotionApplyRequest) (PromotionRollbackResponse, error) {
	if err := validatePromotionRollbackRequest(req); err != nil {
		return PromotionRollbackResponse{}, err
	}
	var out PromotionRollbackResponse
	if err := c.do(ctx, http.MethodPost, "/viewer/sandbox/promotions/rollback", req, &out); err != nil {
		return PromotionRollbackResponse{}, err
	}
	if err := validatePromotionRollbackResponse(out, req.Promotion.RollbackPlanPath); err != nil {
		return PromotionRollbackResponse{}, err
	}
	return out, nil
}

func (c *Client) SubmitPromotionWorkflow(ctx context.Context, req PromotionWorkflowRequest) (PromotionWorkflowResponse, error) {
	if req.ApplyAfterApproval && req.ExternalControl == nil {
		return PromotionWorkflowResponse{SkippedReason: "external control policy is required before apply"}, nil
	}
	if req.ExternalControl != nil {
		controlResp, err := c.CheckExternalControl(ctx, *req.ExternalControl)
		if err != nil {
			return PromotionWorkflowResponse{}, err
		}
		if controlResp.Decision.Status != "allowed" {
			return PromotionWorkflowResponse{SkippedReason: "external control policy did not allow action"}, nil
		}
	}
	promotionResp, err := c.CreatePromotionRequest(ctx, req.Promotion)
	if err != nil {
		return PromotionWorkflowResponse{}, err
	}
	resp := PromotionWorkflowResponse{PromotionResponse: promotionResp}
	if promotionResp.Decision.Status != "approve" {
		resp.SkippedReason = "promotion gate did not approve"
		return resp, nil
	}
	if !req.ApplyAfterApproval {
		resp.SkippedReason = "apply_after_approval is false"
		return resp, nil
	}
	if !req.HumanApproved || strings.TrimSpace(req.Promotion.HumanApprovalStatus) != "granted" {
		resp.SkippedReason = "human approval is required before apply"
		return resp, nil
	}
	if strings.TrimSpace(req.PostApplyVerificationPath) == "" {
		resp.SkippedReason = "post_apply_verification_path is required before apply"
		return resp, nil
	}
	applyReq := PromotionApplyRequest{
		Promotion:                    promotionResp.Promotion,
		AppliedBy:                    req.AppliedBy,
		ApplyTarget:                  req.ApplyTarget,
		PostApplyVerificationPath:    req.PostApplyVerificationPath,
		PostApplyVerificationCommand: req.PostApplyVerificationCommand,
		HumanApproved:                req.HumanApproved,
	}
	if err := validatePromotionApplyRequest(applyReq); err != nil {
		resp.SkippedReason = err.Error()
		return resp, nil
	}
	applyResp, err := c.applyPromotionUnchecked(ctx, applyReq)
	if err != nil {
		return PromotionWorkflowResponse{}, err
	}
	resp.ApplyResponse = &applyResp
	if err := validatePromotionApplyResponse(applyResp, applyReq); err != nil {
		resp.SkippedReason = err.Error()
		return resp, nil
	}
	resp.Applied = true
	return resp, nil
}

func validatePromotionRequest(req PromotionRequest) error {
	if strings.TrimSpace(req.PromotionID) == "" {
		return fmt.Errorf("promotion request missing promotion_id")
	}
	if strings.TrimSpace(req.SandboxID) == "" {
		return fmt.Errorf("promotion request missing sandbox_id")
	}
	if strings.TrimSpace(req.TargetPath) == "" {
		return fmt.Errorf("promotion request missing target_path")
	}
	return nil
}

func validatePromotionApplyRequest(req PromotionApplyRequest) error {
	if err := validatePromotionRequest(req.Promotion); err != nil {
		return err
	}
	if !req.HumanApproved || strings.TrimSpace(req.Promotion.HumanApprovalStatus) != "granted" {
		return fmt.Errorf("promotion apply request requires human approval")
	}
	if strings.TrimSpace(req.Promotion.DiffPath) == "" {
		return fmt.Errorf("promotion apply request missing diff_path")
	}
	if strings.TrimSpace(req.PostApplyVerificationPath) == "" {
		return fmt.Errorf("promotion apply request missing post_apply_verification_path")
	}
	return nil
}

func validatePromotionRollbackRequest(req PromotionApplyRequest) error {
	if err := validatePromotionRequest(req.Promotion); err != nil {
		return err
	}
	if !req.HumanApproved || strings.TrimSpace(req.Promotion.HumanApprovalStatus) != "granted" {
		return fmt.Errorf("promotion rollback request requires human approval")
	}
	if strings.TrimSpace(req.Promotion.RollbackPlanPath) == "" {
		return fmt.Errorf("promotion rollback request missing rollback_plan_path")
	}
	return nil
}

func isSandboxGateStatus(status string) bool {
	switch status {
	case "approve", "reject", "needs_review", "needs_more_tests", "promotion_applied", "rollback_executed":
		return true
	default:
		return false
	}
}

func validatePromotionRequestResponse(resp PromotionRequestResponse, req PromotionRequest) error {
	if strings.TrimSpace(req.PromotionID) != "" && strings.TrimSpace(resp.Promotion.PromotionID) != strings.TrimSpace(req.PromotionID) {
		return fmt.Errorf("promotion request response promotion_id mismatch")
	}
	if strings.TrimSpace(req.SandboxID) != "" && strings.TrimSpace(resp.Promotion.SandboxID) != strings.TrimSpace(req.SandboxID) {
		return fmt.Errorf("promotion request response sandbox_id mismatch")
	}
	if strings.TrimSpace(req.TargetPath) != "" && strings.TrimSpace(resp.Promotion.TargetPath) != strings.TrimSpace(req.TargetPath) {
		return fmt.Errorf("promotion request response target_path mismatch")
	}
	if strings.TrimSpace(req.DiffPath) != "" && strings.TrimSpace(resp.Promotion.DiffPath) != strings.TrimSpace(req.DiffPath) {
		return fmt.Errorf("promotion request response diff_path mismatch")
	}
	if strings.TrimSpace(resp.GateLog.EventID) == "" {
		return fmt.Errorf("promotion request response missing gate event_id")
	}
	if strings.TrimSpace(resp.GateLog.PromotionID) != strings.TrimSpace(resp.Promotion.PromotionID) {
		return fmt.Errorf("promotion request response gate promotion_id mismatch")
	}
	if strings.TrimSpace(resp.GateLog.GateStatus) != strings.TrimSpace(resp.Decision.Status) {
		return fmt.Errorf("promotion request response gate status mismatch")
	}
	switch strings.TrimSpace(resp.Decision.Status) {
	case "approve", "reject":
		if len(resp.Decision.MissingRequirements) > 0 {
			return fmt.Errorf("promotion request response terminal decision includes missing requirements")
		}
	case "needs_review", "needs_more_tests":
		if len(resp.Decision.MissingRequirements) == 0 {
			return fmt.Errorf("promotion request response review decision without missing requirements")
		}
	default:
		return fmt.Errorf("promotion request response invalid decision status: %q", resp.Decision.Status)
	}
	if resp.RollbackArtifact != nil {
		if strings.TrimSpace(resp.RollbackArtifact.Type) != "rollback_plan" {
			return fmt.Errorf("promotion request response rollback artifact type mismatch")
		}
		if strings.TrimSpace(req.RollbackPlanPath) != "" && strings.TrimSpace(resp.RollbackArtifact.FilePath) != strings.TrimSpace(req.RollbackPlanPath) {
			return fmt.Errorf("promotion request response rollback artifact path mismatch")
		}
	}
	if resp.PostApplyVerificationArtifact != nil {
		if strings.TrimSpace(resp.PostApplyVerificationArtifact.Type) != "post_apply_verification" {
			return fmt.Errorf("promotion request response post-apply artifact type mismatch")
		}
		if strings.TrimSpace(req.PostApplyVerificationPath) != "" && strings.TrimSpace(resp.PostApplyVerificationArtifact.FilePath) != strings.TrimSpace(req.PostApplyVerificationPath) {
			return fmt.Errorf("promotion request response post-apply artifact path mismatch")
		}
	}
	if resp.Promotion.CreatedAt.IsZero() {
		return fmt.Errorf("promotion request response promotion missing created_at")
	}
	if resp.GateLog.CreatedAt.IsZero() {
		return fmt.Errorf("promotion request response gate_log missing created_at")
	}
	return nil
}

func validatePromotionApplyResponse(resp PromotionApplyResponse, req PromotionApplyRequest) error {
	if resp.Decision.Status != "promotion_applied" {
		return fmt.Errorf("promotion apply response did not confirm promotion_applied")
	}
	if resp.GateLog.GateStatus != "promotion_applied" {
		return fmt.Errorf("promotion apply gate log did not confirm promotion_applied")
	}
	if strings.TrimSpace(req.Promotion.PromotionID) != "" && strings.TrimSpace(resp.GateLog.PromotionID) != strings.TrimSpace(req.Promotion.PromotionID) {
		return fmt.Errorf("promotion apply gate log promotion_id mismatch")
	}
	if strings.TrimSpace(req.PostApplyVerificationPath) != "" && strings.TrimSpace(resp.GateLog.PostApplyVerification) != "" && strings.TrimSpace(resp.GateLog.PostApplyVerification) != strings.TrimSpace(req.PostApplyVerificationPath) {
		return fmt.Errorf("promotion apply gate log post_apply_verification mismatch")
	}
	artifact := resp.PostApplyVerificationArtifact
	if artifact.Type != "post_apply_verification" || artifact.Status != "completed" {
		return fmt.Errorf("promotion apply response did not include completed post_apply_verification artifact")
	}
	if strings.TrimSpace(req.Promotion.SandboxID) != "" && strings.TrimSpace(artifact.SandboxID) != strings.TrimSpace(req.Promotion.SandboxID) {
		return fmt.Errorf("promotion apply response post_apply_verification artifact sandbox_id mismatch")
	}
	if strings.TrimSpace(req.PostApplyVerificationPath) != "" && strings.TrimSpace(artifact.FilePath) != strings.TrimSpace(req.PostApplyVerificationPath) {
		return fmt.Errorf("promotion apply response post_apply_verification artifact path mismatch")
	}
	if resp.DiffApplyResult != nil && resp.DiffApplyResult.Status != "applied" {
		return fmt.Errorf("promotion apply diff result did not confirm applied")
	}
	if resp.DiffApplyResult != nil && len(resp.DiffApplyResult.AppliedFiles) == 0 {
		return fmt.Errorf("promotion apply diff result missing applied_files")
	}
	if resp.GateLog.CreatedAt.IsZero() {
		return fmt.Errorf("promotion apply response gate_log missing created_at")
	}
	if artifact.CreatedAt.IsZero() {
		return fmt.Errorf("promotion apply response artifact missing created_at")
	}
	return nil
}

func validatePromotionRollbackResponse(resp PromotionRollbackResponse, rollbackPlanPath string) error {
	if resp.Decision.Status != "rollback_executed" {
		return fmt.Errorf("promotion rollback response did not confirm rollback_executed")
	}
	if resp.GateLog.GateStatus != "rollback_executed" {
		return fmt.Errorf("promotion rollback gate log did not confirm rollback_executed")
	}
	if resp.RollbackResult.Status != "rolled_back" {
		return fmt.Errorf("promotion rollback diff result did not confirm rolled_back")
	}
	artifact := resp.RollbackArtifact
	if artifact.Type != "rollback_execution" || artifact.Status != "completed" {
		return fmt.Errorf("promotion rollback response did not include completed rollback_execution artifact")
	}
	if strings.TrimSpace(rollbackPlanPath) != "" && artifact.FilePath != rollbackPlanPath {
		return fmt.Errorf("promotion rollback response rollback_execution artifact path mismatch")
	}
	if resp.GateLog.CreatedAt.IsZero() {
		return fmt.Errorf("promotion rollback response gate_log missing created_at")
	}
	if artifact.CreatedAt.IsZero() {
		return fmt.Errorf("promotion rollback response artifact missing created_at")
	}
	return nil
}

func validateRuntimeConfig(resp RuntimeConfig) error {
	if resp.LLMOpsEnabled && !resp.LLMOpsConfigured {
		return fmt.Errorf("runtime config has llm_ops_enabled without llm_ops_configured")
	}
	if resp.LLMOpsEnabled && strings.TrimSpace(resp.LLMOpsBaseURL) == "" {
		return fmt.Errorf("runtime config has llm_ops_enabled without llm_ops_base_url")
	}
	if resp.LocalLLM.Enabled {
		if strings.TrimSpace(resp.LocalLLM.Provider) == "" {
			return fmt.Errorf("runtime config local_llm enabled missing provider")
		}
		if strings.TrimSpace(resp.LocalLLM.ChatBaseURL) == "" {
			return fmt.Errorf("runtime config local_llm enabled missing chat_base_url")
		}
		if strings.TrimSpace(resp.LocalLLM.WorkerBaseURL) == "" {
			return fmt.Errorf("runtime config local_llm enabled missing worker_base_url")
		}
	}
	if err := validateOptionalRuntimeURL(resp.STTBaseURL, "stt_base_url"); err != nil {
		return err
	}
	if err := validateOptionalRuntimeURL(resp.TTSBaseURL, "tts_base_url"); err != nil {
		return err
	}
	if err := validateOptionalRuntimeURL(resp.LLMOpsBaseURL, "llm_ops_base_url"); err != nil {
		return err
	}
	if strings.TrimSpace(resp.STTStreamURL) != "" {
		u, err := url.Parse(strings.TrimSpace(resp.STTStreamURL))
		if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
			return fmt.Errorf("runtime config has invalid stt_stream_url")
		}
	}
	if strings.TrimSpace(resp.TTSHealthPath) != "" && !strings.HasPrefix(strings.TrimSpace(resp.TTSHealthPath), "/") {
		return fmt.Errorf("runtime config tts_health_path must be absolute")
	}
	if resp.RuntimeReadiness.SlackCredentialsPresent == nil ||
		resp.RuntimeReadiness.SlackWebhookRegistered == nil ||
		resp.RuntimeReadiness.SlackFilePayloadPipeline == nil ||
		resp.RuntimeReadiness.DiscordCredentialsPresent == nil ||
		resp.RuntimeReadiness.DiscordWebhookRegistered == nil ||
		resp.RuntimeReadiness.DiscordFilePayloadPipeline == nil ||
		resp.RuntimeReadiness.TelegramCredentialsPresent == nil ||
		resp.RuntimeReadiness.TelegramWebhookRegistered == nil ||
		resp.RuntimeReadiness.TelegramFilePayloadPipeline == nil ||
		resp.RuntimeReadiness.STTGatewayEnvPresent == nil ||
		resp.RuntimeReadiness.STTGatewayConfigPresent == nil ||
		resp.RuntimeReadiness.TTSProviderEnvPresent == nil ||
		resp.RuntimeReadiness.TTSProviderConfigPresent == nil ||
		resp.RuntimeReadiness.DistributedEnabled == nil ||
		resp.RuntimeReadiness.DistributedTransportsPresent == nil ||
		resp.RuntimeReadiness.DistributedSSHConfigured == nil ||
		resp.RuntimeReadiness.DistributedSSHConnected == nil ||
		resp.RuntimeReadiness.DistributedLocalTransport == nil ||
		resp.RuntimeReadiness.ConversationEnabled == nil ||
		resp.RuntimeReadiness.L1SQLiteConfigPresent == nil ||
		resp.RuntimeReadiness.MemoryLayersAvailable == nil ||
		resp.RuntimeReadiness.MemoryLayersStatus == nil ||
		resp.RuntimeReadiness.SourceRegistryAvailable == nil ||
		resp.RuntimeReadiness.SourceRegistryStatus == nil ||
		resp.RuntimeReadiness.DomainGraphAvailable == nil ||
		resp.RuntimeReadiness.DomainGraphStatus == nil ||
		resp.RuntimeReadiness.KnowledgeMemoryEnabled == nil ||
		resp.RuntimeReadiness.KnowledgeMemoryStatus == nil ||
		resp.RuntimeReadiness.BrowserTraceAPIEnabled == nil ||
		resp.RuntimeReadiness.BrowserTraceAPIStatus == nil ||
		resp.RuntimeReadiness.BrowserTraceAPIFetcher == nil ||
		resp.RuntimeReadiness.SandboxEnabled == nil ||
		resp.RuntimeReadiness.SandboxStatusAvailable == nil {
		return fmt.Errorf("runtime config missing runtime_readiness fields")
	}
	hasSTTConfig := strings.TrimSpace(resp.STTBaseURL) != "" || strings.TrimSpace(resp.STTStreamURL) != ""
	if hasSTTConfig != *resp.RuntimeReadiness.STTGatewayConfigPresent {
		return fmt.Errorf("runtime config stt_gateway_config_present mismatch")
	}
	hasTTSConfig := strings.TrimSpace(resp.TTSBaseURL) != ""
	if hasTTSConfig != *resp.RuntimeReadiness.TTSProviderConfigPresent {
		return fmt.Errorf("runtime config tts_provider_config_present mismatch")
	}
	if *resp.RuntimeReadiness.SourceRegistryAvailable && (!*resp.RuntimeReadiness.ConversationEnabled || !*resp.RuntimeReadiness.L1SQLiteConfigPresent) {
		return fmt.Errorf("runtime config source_registry_available requires conversation and l1 sqlite config")
	}
	if *resp.RuntimeReadiness.MemoryLayersAvailable && (!*resp.RuntimeReadiness.ConversationEnabled || !*resp.RuntimeReadiness.L1SQLiteConfigPresent || !*resp.RuntimeReadiness.MemoryLayersStatus) {
		return fmt.Errorf("runtime config memory_layers_available requires conversation, l1 sqlite config, and status route")
	}
	if *resp.RuntimeReadiness.SourceRegistryAvailable && !*resp.RuntimeReadiness.SourceRegistryStatus {
		return fmt.Errorf("runtime config source_registry_available requires source_registry_status_available")
	}
	if *resp.RuntimeReadiness.DomainGraphAvailable && (!*resp.RuntimeReadiness.ConversationEnabled || !*resp.RuntimeReadiness.L1SQLiteConfigPresent || !*resp.RuntimeReadiness.DomainGraphStatus) {
		return fmt.Errorf("runtime config domain_graph_available requires conversation, l1 sqlite config, and domain_graph_status_available")
	}
	if *resp.RuntimeReadiness.KnowledgeMemoryEnabled && !*resp.RuntimeReadiness.KnowledgeMemoryStatus {
		return fmt.Errorf("runtime config knowledge_memory_enabled requires knowledge_memory_status_available")
	}
	if *resp.RuntimeReadiness.BrowserTraceAPIEnabled && !*resp.RuntimeReadiness.BrowserTraceAPIStatus {
		return fmt.Errorf("runtime config browser_trace_api_enabled requires browser_trace_api_status_available")
	}
	if *resp.RuntimeReadiness.BrowserTraceAPIFetcher && (!*resp.RuntimeReadiness.BrowserTraceAPIEnabled || !*resp.RuntimeReadiness.BrowserTraceAPIStatus) {
		return fmt.Errorf("runtime config browser_trace_api_fetcher_available requires browser trace status route")
	}
	if *resp.RuntimeReadiness.SandboxEnabled && !*resp.RuntimeReadiness.SandboxStatusAvailable {
		return fmt.Errorf("runtime config sandbox_enabled requires sandbox_status_available")
	}
	if *resp.RuntimeReadiness.SlackFilePayloadPipeline && !*resp.RuntimeReadiness.SlackWebhookRegistered {
		return fmt.Errorf("runtime config slack_file_payload_pipeline requires slack_webhook_registered")
	}
	if *resp.RuntimeReadiness.DiscordFilePayloadPipeline && !*resp.RuntimeReadiness.DiscordWebhookRegistered {
		return fmt.Errorf("runtime config discord_file_payload_pipeline requires discord_webhook_registered")
	}
	if *resp.RuntimeReadiness.TelegramFilePayloadPipeline && !*resp.RuntimeReadiness.TelegramWebhookRegistered {
		return fmt.Errorf("runtime config telegram_file_payload_pipeline requires telegram_webhook_registered")
	}
	if *resp.RuntimeReadiness.DistributedSSHConnected && (!*resp.RuntimeReadiness.DistributedEnabled || !*resp.RuntimeReadiness.DistributedSSHConfigured) {
		return fmt.Errorf("runtime config distributed_ssh_connected requires distributed enabled and ssh configured")
	}
	if *resp.RuntimeReadiness.DistributedLocalTransport && !*resp.RuntimeReadiness.DistributedEnabled {
		return fmt.Errorf("runtime config distributed_local_transport requires distributed enabled")
	}
	return nil
}

func validateRuntimeHealth(resp RuntimeHealthReport, httpStatus int) error {
	status := strings.TrimSpace(resp.Status)
	if !isRuntimeHealthStatus(status) {
		return fmt.Errorf("runtime health invalid status=%q", resp.Status)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(resp.Timestamp)); err != nil {
		return fmt.Errorf("runtime health timestamp must be RFC3339: %w", err)
	}
	if status == "down" && httpStatus != http.StatusServiceUnavailable {
		return fmt.Errorf("runtime health http status=%d inconsistent with down report", httpStatus)
	}
	if status != "down" && httpStatus != http.StatusOK {
		return fmt.Errorf("runtime health http status=%d inconsistent with %s report", httpStatus, status)
	}
	if status != "ok" && len(resp.Checks) == 0 {
		return fmt.Errorf("runtime health %s report missing checks", status)
	}
	derived := "ok"
	for _, check := range resp.Checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			return fmt.Errorf("runtime health check missing name")
		}
		checkStatus := strings.TrimSpace(check.Status)
		if !isRuntimeHealthStatus(checkStatus) {
			return fmt.Errorf("runtime health check %q invalid check status=%q", name, check.Status)
		}
		if check.DurationMS < 0 {
			return fmt.Errorf("runtime health check %q has negative duration_ms", name)
		}
		if checkStatus != "ok" && strings.TrimSpace(check.Message) == "" {
			return fmt.Errorf("runtime health check %q missing message", name)
		}
		if checkStatus == "down" {
			derived = "down"
		} else if checkStatus == "degraded" && derived != "down" {
			derived = "degraded"
		}
	}
	if len(resp.Checks) > 0 && status != derived {
		return fmt.Errorf("runtime health overall status=%q inconsistent with checks=%q", status, derived)
	}
	return nil
}

func isRuntimeHealthStatus(status string) bool {
	switch status {
	case "ok", "degraded", "down":
		return true
	default:
		return false
	}
}

func validateLLMOpsStatus(resp LLMOpsStatus) error {
	if len(resp.Roles) == 0 {
		return fmt.Errorf("llm ops status missing roles")
	}
	for role, state := range resp.Roles {
		role = strings.TrimSpace(role)
		if !isLLMOpsRole(role) {
			return fmt.Errorf("llm ops status unknown role %q", role)
		}
		if state.HealthOK == nil {
			return fmt.Errorf("llm ops status role %q missing health_ok", role)
		}
		if state.Halted != nil && *state.Halted && *state.HealthOK {
			return fmt.Errorf("llm ops status role %q halted but health_ok", role)
		}
		if mem, ok := resp.Memory.LLMByRole[role]; ok {
			if strings.TrimSpace(mem.Role) != "" && strings.TrimSpace(mem.Role) != role {
				return fmt.Errorf("llm ops status memory role %q has mismatched role %q", role, mem.Role)
			}
			if mem.Port < 0 {
				return fmt.Errorf("llm ops status memory role %q has negative port", role)
			}
			if mem.RSSMiB < 0 {
				return fmt.Errorf("llm ops status memory role %q has negative rss_mib", role)
			}
			if mem.PID != nil && *mem.PID < 0 {
				return fmt.Errorf("llm ops status memory role %q has negative pid", role)
			}
			if state.Halted != nil && *state.Halted && mem.PID != nil && *mem.PID > 0 {
				return fmt.Errorf("llm ops status role %q halted but pid is present", role)
			}
		}
	}
	for role := range resp.Memory.LLMByRole {
		trimmedRole := strings.TrimSpace(role)
		if !isLLMOpsRole(trimmedRole) {
			return fmt.Errorf("llm ops status memory unknown role %q", role)
		}
		if _, ok := resp.Roles[trimmedRole]; !ok {
			return fmt.Errorf("llm ops status memory role %q missing role state", trimmedRole)
		}
	}
	return nil
}

func validateLLMOpsHealth(resp LLMOpsHealth) error {
	status := strings.TrimSpace(resp.Status)
	if status == "" {
		return fmt.Errorf("llm ops health missing status")
	}
	if !isRuntimeHealthStatus(status) {
		return fmt.Errorf("llm ops health invalid status=%q", resp.Status)
	}
	return nil
}

func normalizeLLMOpsRoles(roles []string) ([]string, error) {
	if len(roles) == 0 {
		return nil, fmt.Errorf("llm ops control roles are required")
	}
	out := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if !isLLMOpsRole(role) {
			return nil, fmt.Errorf("llm ops control invalid role %q", role)
		}
		out = append(out, role)
	}
	return out, nil
}

func normalizeLLMOpsSelection(selection string, allowAll bool) (string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return "", fmt.Errorf("llm ops control selection is required")
	}
	if selection == "all" && allowAll {
		return selection, nil
	}
	if !isLLMOpsRole(selection) {
		return "", fmt.Errorf("llm ops control invalid selection %q", selection)
	}
	return selection, nil
}

func isLLMOpsRole(role string) bool {
	switch role {
	case "Chat", "Worker", "Heavy", "Wild":
		return true
	default:
		return false
	}
}

func validateDebugSystemSnapshot(resp DebugSystemSnapshot) error {
	if strings.TrimSpace(resp.UpdatedAt) == "" {
		return fmt.Errorf("debug system snapshot missing updated_at")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(resp.UpdatedAt)); err != nil {
		return fmt.Errorf("debug system snapshot invalid updated_at: %w", err)
	}
	if err := validateOptionalRuntimeURL(resp.Audio.STTBaseURL, "debug audio stt_base_url"); err != nil {
		return err
	}
	if err := validateOptionalRuntimeURL(resp.Audio.TTSBaseURL, "debug audio tts_base_url"); err != nil {
		return err
	}
	if resp.Audio.STTOK && strings.TrimSpace(resp.Audio.STTBaseURL) == "" {
		return fmt.Errorf("debug system snapshot claims stt_ok without stt_base_url")
	}
	if (resp.Audio.TTSLiveOK || resp.Audio.TTSReadyOK) && strings.TrimSpace(resp.Audio.TTSBaseURL) == "" {
		return fmt.Errorf("debug system snapshot claims tts ok without tts_base_url")
	}
	if resp.Audio.TTSReadyOK && !resp.Audio.TTSLiveOK {
		return fmt.Errorf("debug system snapshot claims tts_ready_ok without tts_live_ok")
	}
	if strings.TrimSpace(resp.Audio.STTBaseURL) != "" && !resp.Audio.STTOK &&
		strings.TrimSpace(resp.Audio.STTHealth) == "" && strings.TrimSpace(resp.Audio.LastError) == "" {
		return fmt.Errorf("debug system snapshot stt down without health or error evidence")
	}
	if strings.TrimSpace(resp.Audio.TTSBaseURL) != "" && !resp.Audio.TTSLiveOK && !resp.Audio.TTSReadyOK &&
		strings.TrimSpace(resp.Audio.TTSLive) == "" && strings.TrimSpace(resp.Audio.TTSReady) == "" &&
		strings.TrimSpace(resp.Audio.LastError) == "" {
		return fmt.Errorf("debug system snapshot tts down without live/ready or error evidence")
	}
	return nil
}

func validateOptionalRuntimeURL(raw string, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("runtime config has invalid %s", field)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method string, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &APIError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: string(data)}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
