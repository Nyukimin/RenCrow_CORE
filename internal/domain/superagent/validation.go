package superagent

import (
	"fmt"
	"strings"
)

func ValidateAgentRun(item AgentRun) error {
	if strings.TrimSpace(item.RunID) == "" {
		return fmt.Errorf("run_id is required")
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
	if strings.TrimSpace(item.RunID) == "" {
		return fmt.Errorf("run_id is required")
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

func ValidateTraceEvent(item TraceEvent) error {
	if strings.TrimSpace(item.EventID) == "" {
		return fmt.Errorf("event_id is required")
	}
	if strings.TrimSpace(item.EventType) == "" {
		return fmt.Errorf("event_type is required")
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
