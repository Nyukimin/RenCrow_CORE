package superagent

import (
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type AgentRun struct {
	RunID              modulecore.RunID  `json:"run_id"`
	TaskID             modulecore.TaskID `json:"task_id"`
	WorkstreamID       string            `json:"workstream_id,omitempty"`
	AgentType          string            `json:"agent_type"`
	Goal               string            `json:"goal,omitempty"`
	Status             string            `json:"status"`
	StartedAt          time.Time         `json:"started_at"`
	CompletedAt        time.Time         `json:"completed_at,omitempty"`
	Summary            string            `json:"summary,omitempty"`
	ResumePolicy       string            `json:"resume_policy,omitempty"`
	CheckpointRevision int               `json:"checkpoint_revision,omitempty"`
	CheckpointSummary  string            `json:"checkpoint_summary,omitempty"`
	NextAction         string            `json:"next_action,omitempty"`
	LastCheckpointAt   time.Time         `json:"last_checkpoint_at,omitempty"`
}

type SubagentTask struct {
	TaskID               modulecore.TaskID `json:"task_id"`
	RunID                modulecore.RunID  `json:"run_id"`
	ActorID              string            `json:"actor_id"`
	Task                 string            `json:"task"`
	Scope                []string          `json:"scope"`
	Tools                []string          `json:"tools,omitempty"`
	TerminationCondition string            `json:"termination_condition"`
	OutputPath           string            `json:"output_path,omitempty"`
	Status               string            `json:"status"`
	CreatedAt            time.Time         `json:"created_at"`
	CompletedAt          time.Time         `json:"completed_at,omitempty"`
}

type ContextPack struct {
	ContextPackID   string            `json:"context_pack_id"`
	TaskID          modulecore.TaskID `json:"task_id"`
	RunID           modulecore.RunID  `json:"run_id"`
	WorkstreamID    string            `json:"workstream_id,omitempty"`
	Summary         string            `json:"summary"`
	IncludedSources []string          `json:"included_sources,omitempty"`
	TokenEstimate   int               `json:"token_estimate,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
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

type RunQueueItem struct {
	QueueID            string    `json:"queue_id"`
	RunID              string    `json:"run_id,omitempty"`
	WorkstreamID       string    `json:"workstream_id,omitempty"`
	Goal               string    `json:"goal"`
	Action             string    `json:"action"`
	Status             string    `json:"status"`
	Priority           int       `json:"priority,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	NotBefore          time.Time `json:"not_before,omitempty"`
	ClaimedAt          time.Time `json:"claimed_at,omitempty"`
	LeaseToken         string    `json:"lease_token,omitempty"`
	LeaseUntil         time.Time `json:"lease_until,omitempty"`
	AttemptCount       int       `json:"attempt_count,omitempty"`
	CheckpointRevision int       `json:"checkpoint_revision,omitempty"`
	CheckpointSummary  string    `json:"checkpoint_summary,omitempty"`
	NextAction         string    `json:"next_action,omitempty"`
	IdempotencyKey     string    `json:"idempotency_key,omitempty"`
	CompletedAt        time.Time `json:"completed_at,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}
