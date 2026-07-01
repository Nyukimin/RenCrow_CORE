package superagent

import "time"

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
