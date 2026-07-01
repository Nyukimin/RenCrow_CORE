package aiworkflow

import "time"

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
	RunID           string    `json:"run_id,omitempty"`
	WorkstreamID    string    `json:"workstream_id,omitempty"`
	JobID           string    `json:"job_id,omitempty"`
	CompactionID    string    `json:"compaction_id,omitempty"`
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
