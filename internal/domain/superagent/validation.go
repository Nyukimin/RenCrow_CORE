package superagent

import (
	"encoding/hex"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func ValidateAgentRun(item AgentRun) error {
	if err := item.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if err := item.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	// Lumina is a user-confirmed historical attribution, not a current runtime
	// assignee. Only terminal history can retain it; execution admission still
	// uses ValidateActorID without a historical exception.
	if item.ActorID != "lumina" || !isAgentRunTerminalStatus(item.Status) {
		if err := ValidateActorID(item.ActorID); err != nil {
			return err
		}
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
		if err := item.CheckpointID.Validate(); err != nil {
			return fmt.Errorf("checkpoint_id is invalid: %w", err)
		}
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
	if err := item.ArtifactID.Validate(); err != nil {
		return fmt.Errorf("artifact_id is invalid: %w", err)
	}
	if item.Kind != modulecore.ArtifactKindContextPack {
		return fmt.Errorf("artifact_kind must be %q", modulecore.ArtifactKindContextPack)
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
	// The content digest and the supersession edge are checked last so an identity
	// defect keeps reporting its own field. The digest covers only the body declared
	// once in contextpack_content.go, and the shared canonical validators in
	// modules/core own the hash form and the supersession direction.
	if err := modulecore.ValidateContentHash(item.ContentHash, "content_hash"); err != nil {
		return err
	}
	if got := ComputeContextPackContentHash(item); got != item.ContentHash {
		return fmt.Errorf("content_hash %s does not match content digest %s", item.ContentHash, got)
	}
	if err := modulecore.ValidateArtifactSupersession(item.ArtifactID, item.SupersededBy); err != nil {
		return err
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
	if err := item.QueueItemID.Validate(); err != nil {
		return fmt.Errorf("queue_item_id is invalid: %w", err)
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
	if item.PredecessorRunID != "" {
		if err := item.PredecessorRunID.Validate(); err != nil {
			return fmt.Errorf("predecessor_run_id is invalid: %w", err)
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
	if hasPrivateTextResumeMetadata(item) && !IsPrivateTextResumeQueueItem(item) {
		return fmt.Errorf("private text checkpoint resume intent is invalid")
	}
	if isCheckpointResumeQueueItem(item) {
		if err := item.CheckpointID.Validate(); err != nil {
			return fmt.Errorf("checkpoint_id is invalid: %w", err)
		}
	}
	return nil
}

// PrivateTextResumeQueueKey binds one insert-only private text resume intent to
// the exact predecessor Run. Queue summaries remain generic; the checkpoint
// content and digest are never projected into goal, summary, or trace fields.
func PrivateTextResumeQueueKey(predecessor modulecore.RunID) string {
	return RunQueueIntentPrivateTextResumePrefix + string(predecessor)
}

// IsPrivateTextResumeQueueItem recognizes only the distinct private text
// continuation intent. Generic Viewer resume items keep their existing
// checkpoint_id/revision contract and do not match this predicate.
func IsPrivateTextResumeQueueItem(item RunQueueItem) bool {
	if item.RunStartReason != domaintask.RunStartReasonCheckpointResume ||
		item.Goal != RunQueueGoalPrivateFirstChat || item.Action != "chat" ||
		item.PredecessorRunID == "" || item.IdempotencyKey != PrivateTextResumeQueueKey(item.PredecessorRunID) ||
		!validCheckpointDigest(item.CheckpointDigest) {
		return false
	}
	return item.PredecessorRunID.Validate() == nil
}

func hasPrivateTextResumeMetadata(item RunQueueItem) bool {
	return item.PredecessorRunID != "" || item.CheckpointDigest != "" ||
		strings.HasPrefix(item.IdempotencyKey, RunQueueIntentPrivateTextResumePrefix)
}

func validCheckpointDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func isCheckpointResumeQueueItem(item RunQueueItem) bool {
	switch item.RunStartReason {
	case domaintask.RunStartReasonCheckpointResume, domaintask.RunStartReasonProcessRestartResume:
		return strings.TrimSpace(item.Action) == "resume"
	default:
		return false
	}
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

var privateChatQueueActorIDs = [...]string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}

// PrivateChatQueueActorIDs returns the exact assignees eligible for the
// private first-CHAT queue. This is deliberately narrower than CORE Actor
// identity and does not grant delegation or full-access execution rights.
func PrivateChatQueueActorIDs() []string {
	return append([]string(nil), privateChatQueueActorIDs[:]...)
}

// IsPrivateChatQueueActorID reports whether actorID is one of the exact
// assignee identities supported by the private CHAT queue.
func IsPrivateChatQueueActorID(actorID string) bool {
	for _, candidate := range privateChatQueueActorIDs {
		if actorID == candidate {
			return true
		}
	}
	return false
}

// IsPrivateChatQueueTask recognizes the local, unparented CHAT Task shape
// accepted by the private text queue. The caller still checks the exact owner
// and lifecycle status for its own boundary.
func IsPrivateChatQueueTask(task domaintask.Task) bool {
	return task.Route == domaintask.RouteCHAT && task.OwnerID != "" && strings.TrimSpace(task.OwnerID) == task.OwnerID &&
		task.OriginSessionID == "" && task.OriginThreadID == "" && task.OriginTurnID == "" && task.OriginMessageID == "" &&
		task.OriginPublication == nil && task.AcceptedOPSClaim == nil && len(task.NativeResumeClaims) == 0 && task.ParentTaskID == ""
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
