package superagent

import (
	"fmt"
	"strings"
)

func ValidateAgentRun(item AgentRun) error {
	if err := item.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if strings.TrimSpace(item.AgentType) == "" {
		return fmt.Errorf("agent_type is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if item.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if isAgentRunTerminalStatus(item.Status) && item.CompletedAt.IsZero() {
		return fmt.Errorf("completed_at is required for terminal agent run")
	}
	if strings.TrimSpace(item.ResumePolicy) != "" && strings.TrimSpace(item.ResumePolicy) != "checkpoint" {
		return fmt.Errorf("resume_policy must be checkpoint when set")
	}
	if strings.TrimSpace(item.ResumePolicy) == "checkpoint" {
		if item.CheckpointRevision <= 0 || strings.TrimSpace(item.CheckpointSummary) == "" || strings.TrimSpace(item.NextAction) == "" || item.LastCheckpointAt.IsZero() {
			return fmt.Errorf("checkpoint resume requires revision, summary, next_action, and last_checkpoint_at")
		}
	}
	return nil
}

func ValidateSubagentTask(item SubagentTask) error {
	if strings.TrimSpace(item.SubagentID) == "" {
		return fmt.Errorf("subagent_id is required")
	}
	if strings.TrimSpace(item.ParentRunID) == "" {
		return fmt.Errorf("parent_run_id is required")
	}
	if strings.TrimSpace(item.AgentType) == "" {
		return fmt.Errorf("agent_type is required")
	}
	if strings.TrimSpace(item.Task) == "" {
		return fmt.Errorf("task is required")
	}
	if len(item.Scope) == 0 {
		return fmt.Errorf("scope is required")
	}
	if strings.TrimSpace(item.TerminationCondition) == "" {
		return fmt.Errorf("termination_condition is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateContextPack(item ContextPack, maxTokens int) error {
	if strings.TrimSpace(item.ContextPackID) == "" {
		return fmt.Errorf("context_pack_id is required")
	}
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := item.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if strings.TrimSpace(item.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if item.TokenEstimate < 0 {
		return fmt.Errorf("token_estimate must be >= 0")
	}
	if maxTokens > 0 && item.TokenEstimate > maxTokens {
		return fmt.Errorf("token_estimate exceeds max_context_pack_tokens")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateMessageChannel(item MessageChannel) error {
	if strings.TrimSpace(item.ChannelID) == "" {
		return fmt.Errorf("channel_id is required")
	}
	if strings.TrimSpace(item.ChannelType) == "" {
		return fmt.Errorf("channel_type is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateRunQueueItem(item RunQueueItem) error {
	if strings.TrimSpace(item.QueueID) == "" {
		return fmt.Errorf("queue_id is required")
	}
	if strings.TrimSpace(item.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	if strings.TrimSpace(item.Action) == "" {
		return fmt.Errorf("action is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if isRunQueueTerminalStatus(item.Status) && item.CompletedAt.IsZero() {
		return fmt.Errorf("completed_at is required for terminal run queue item")
	}
	if item.AttemptCount < 0 || item.CheckpointRevision < 0 {
		return fmt.Errorf("attempt_count and checkpoint_revision must be >= 0")
	}
	if strings.TrimSpace(item.Status) == "claimed" {
		if strings.TrimSpace(item.LeaseToken) == "" || item.LeaseUntil.IsZero() || item.ClaimedAt.IsZero() {
			return fmt.Errorf("claimed run queue item requires lease token, lease_until, and claimed_at")
		}
	}
	return nil
}

func isAgentRunTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "paused":
		return true
	default:
		return false
	}
}

func isRunQueueTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}
