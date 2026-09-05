package superagent

import (
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func ValidateAgentRun(item AgentRun) error {
	if err := item.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := ValidateActorID(item.ActorID); err != nil {
		return err
	}
	if item.Status == "" {
		return fmt.Errorf("status is required")
	}
	if !isAgentRunStatus(item.Status) {
		return fmt.Errorf("status %q is invalid", item.Status)
	}
	if item.StartedAt.IsZero() {
		return fmt.Errorf("started_at is required")
	}
	if item.Status == "running" {
		if !item.CompletedAt.IsZero() {
			return fmt.Errorf("completed_at must be zero for running agent run")
		}
	} else if item.CompletedAt.IsZero() {
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
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := item.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := ValidateActorID(item.ActorID); err != nil {
		return err
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
	if isSubagentTaskTerminalStatus(item.Status) && item.CompletedAt.IsZero() {
		return fmt.Errorf("completed_at is required for terminal subagent task")
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
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if !domaintask.ValidRunStartReason(item.RunStartReason) {
		return fmt.Errorf("run_start_reason is invalid: %q", item.RunStartReason)
	}
	if strings.TrimSpace(item.Goal) == "" {
		return fmt.Errorf("goal is required")
	}
	if strings.TrimSpace(item.Action) == "" {
		return fmt.Errorf("action is required")
	}
	status := strings.TrimSpace(item.Status)
	if status == "" {
		return fmt.Errorf("status is required")
	}
	if !isRunQueueStatus(status) {
		return fmt.Errorf("status %q is invalid", item.Status)
	}
	if item.RunID != "" {
		if err := item.RunID.Validate(); err != nil {
			return fmt.Errorf("run_id is invalid: %w", err)
		}
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if (status == "queued" || status == "reserved") && item.RunID != "" {
		return fmt.Errorf("%s run queue item must not retain run_id", status)
	}
	if status == "reserved" || status == "claimed" {
		if strings.TrimSpace(item.LeaseToken) == "" || item.LeaseUntil.IsZero() || item.ClaimedAt.IsZero() {
			return fmt.Errorf("%s run queue item requires lease token, lease_until, and claimed_at", status)
		}
	}
	if status == "claimed" && item.RunID == "" {
		return fmt.Errorf("run_id is required for claimed run queue item")
	}
	if isRunQueueTerminalStatus(status) {
		if status == "blocked" {
			if item.RunID != "" {
				return fmt.Errorf("blocked run queue item must not retain run_id")
			}
			if strings.TrimSpace(item.Reason) == "" {
				return fmt.Errorf("reason is required for blocked run queue item")
			}
		} else if item.RunID == "" {
			return fmt.Errorf("run_id is required for terminal run queue item")
		}
		if item.CompletedAt.IsZero() {
			return fmt.Errorf("completed_at is required for terminal run queue item")
		}
	}
	if item.AttemptCount < 0 || item.CheckpointRevision < 0 {
		return fmt.Errorf("attempt_count and checkpoint_revision must be >= 0")
	}
	return nil
}

func isRunQueueStatus(status string) bool {
	switch status {
	case "queued", "reserved", "claimed", "completed", "failed", "cancelled", "blocked":
		return true
	default:
		return false
	}
}

func isAgentRunTerminalStatus(status string) bool {
	return status != "running" && isAgentRunStatus(status)
}

func isAgentRunStatus(status string) bool {
	switch status {
	case "running", "paused", "completed", "failed", "cancelled", "blocked", "interrupted", "reassigned", "superseded":
		return true
	default:
		return false
	}
}

// ValidateActorID accepts only the exact identities of CORE-managed Actors.
// Mechanism labels such as LeadAgent, worker, provider, or model names are not
// Actor identities and must never be persisted in a projection.
func ValidateActorID(actorID string) error {
	switch actorID {
	case "mio", "shiro", "midori", "kuro":
		return nil
	default:
		return fmt.Errorf("actor_id must be one of mio, shiro, midori, kuro")
	}
}

func isSubagentTaskTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func isRunQueueTerminalStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "blocked":
		return true
	default:
		return false
	}
}
