package superagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type InterruptedRunRecoveryStore interface {
	ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error)
	SaveAgentRun(context.Context, domainsuperagent.AgentRun) error
	ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error)
	SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
}

// InterruptedRunRecoveryTaskOwner is the canonical Task/Run owner used while
// rebuilding projections after a process restart. Recovery may inspect and
// block through this owner, but it must not synthesize or close a Run from an
// AgentRun or queue receipt.
type InterruptedRunRecoveryTaskOwner interface {
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListRuns(context.Context, domaintask.RunFilter) ([]domaintask.Run, error)
	Block(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
}

const interruptedRunCheckpointUnavailable = "restart resume blocked: durable checkpoint is unavailable"

type interruptedRunRecoveryCandidate struct {
	projection domainsuperagent.AgentRun
	task       domaintask.Task
	run        domaintask.Run
}

// RecoverInterruptedAgentRuns rebuilds only a deterministic resume intent from
// a durable checkpoint. Canonical Task/Run state is authoritative; AgentRun
// and RunQueue are projections/intents and are never allowed to close or
// revive a canonical Run.
func RecoverInterruptedAgentRuns(ctx context.Context, store InterruptedRunRecoveryStore, owner InterruptedRunRecoveryTaskOwner, now time.Time) (queued int, blocked int, err error) {
	if store == nil {
		return 0, 0, fmt.Errorf("interrupted run recovery store is unavailable")
	}
	if owner == nil {
		return 0, 0, fmt.Errorf("interrupted run recovery task owner is unavailable")
	}
	projections, err := store.ListAgentRuns(ctx, 500)
	if err != nil {
		return 0, 0, err
	}
	queue, err := store.ListRunQueueItems(ctx, 500)
	if err != nil {
		return 0, 0, err
	}
	queueByID := make(map[string]domainsuperagent.RunQueueItem, len(queue))
	for _, item := range queue {
		queueByID[item.QueueID] = item
		if key := strings.TrimSpace(item.IdempotencyKey); key != "" {
			queueByID[key] = item
		}
	}

	// Resolve every projection and its exact canonical Task+Run before making
	// any projection or queue mutation. This gives missing/mismatched identity
	// a fail-closed boundary instead of partially repairing earlier rows.
	candidates := make([]interruptedRunRecoveryCandidate, 0, len(projections))
	seen := make(map[string]struct{}, len(projections))
	for _, projection := range projections {
		if err := projection.TaskID.Validate(); err != nil {
			return 0, 0, fmt.Errorf("agent run %s has invalid task_id: %w", projection.RunID, err)
		}
		if err := projection.RunID.Validate(); err != nil {
			return 0, 0, fmt.Errorf("agent run for task %s has invalid run_id: %w", projection.TaskID, err)
		}
		identity := string(projection.TaskID) + "\x00" + string(projection.RunID)
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}

		task, err := owner.Get(ctx, projection.TaskID)
		if err != nil {
			return 0, 0, fmt.Errorf("get canonical task %s for interrupted run recovery: %w", projection.TaskID, err)
		}
		if task.TaskID != projection.TaskID {
			return 0, 0, fmt.Errorf("canonical task identity mismatch: got %s, want %s", task.TaskID, projection.TaskID)
		}
		runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: projection.TaskID})
		if err != nil {
			return 0, 0, fmt.Errorf("list canonical runs for task %s during interrupted run recovery: %w", projection.TaskID, err)
		}
		canonicalRun, err := exactInterruptedRun(runs, projection.TaskID, projection.RunID)
		if err != nil {
			return 0, 0, err
		}
		if err := validateInterruptedRunState(task, canonicalRun); err != nil {
			return 0, 0, err
		}
		candidates = append(candidates, interruptedRunRecoveryCandidate{projection: projection, task: task, run: canonicalRun})
	}

	// The Viewer changes a waiting Task to queued before it persists the
	// checkpoint-resume intent. Validate an already-present intent before any
	// projection repair so a malformed cross-store match fails closed without a
	// partial recovery write.
	for _, candidate := range candidates {
		if !isWaitingCheckpointResumeCandidate(candidate) {
			continue
		}
		queueID := interruptedRunResumeQueueID(candidate.projection)
		existing, ok := queueByID[queueID]
		if !ok {
			continue
		}
		if err := validateCheckpointResumeQueueIntent(existing, checkpointResumeQueueItem(candidate.projection, now)); err != nil {
			return 0, 0, err
		}
	}
	for _, candidate := range candidates {
		if !isProcessRestartResumeCandidate(candidate) {
			continue
		}
		queueID := interruptedRunResumeQueueID(candidate.projection)
		existing, ok := queueByID[queueID]
		if !ok {
			continue
		}
		if err := validateCheckpointResumeQueueIntent(existing, processRestartResumeQueueItem(candidate.projection, now)); err != nil {
			return 0, 0, err
		}
	}

	// Repair terminal projections before blocking an invalid active checkpoint.
	// Blocking changes the canonical Task/Run, so keeping all reads above the
	// mutation boundary also avoids observing a half-repaired identity.
	for _, candidate := range candidates {
		if candidate.run.Status == domaintask.RunStatusRunning {
			continue
		}
		status, ok := agentRunStatusForCanonicalRun(candidate.run.Status)
		if !ok {
			return queued, blocked, fmt.Errorf("canonical run %s has unsupported recovery status %q", candidate.run.RunID, candidate.run.Status)
		}
		if err := repairAgentRunProjection(ctx, store, candidate.projection, status, canonicalRunSummary(candidate.run), canonicalRunCompletedAt(candidate.run)); err != nil {
			return queued, blocked, err
		}
	}

	for _, candidate := range candidates {
		if !isWaitingCheckpointResumeCandidate(candidate) {
			continue
		}
		queueID := interruptedRunResumeQueueID(candidate.projection)
		if _, ok := queueByID[queueID]; ok {
			continue
		}
		item := checkpointResumeQueueItem(candidate.projection, now)
		if err := store.SaveRunQueueItem(ctx, item); err != nil {
			return queued, blocked, err
		}
		queueByID[queueID] = item
		queued++
	}

	for _, candidate := range candidates {
		if candidate.run.Status != domaintask.RunStatusRunning || candidate.task.Status != domaintask.StatusRunning || candidate.projection.Status != "running" {
			// A queue intent is recoverable only when both canonical owner records
			// and the projection agree that the exact execution was running.
			continue
		}
		if !hasDurableCheckpoint(candidate.projection) {
			blockedTask, blockErr := owner.Block(ctx, candidate.projection.TaskID, interruptedRunCheckpointUnavailable)
			if blockErr != nil {
				return queued, blocked, fmt.Errorf("block canonical task %s during interrupted run recovery: %w", candidate.projection.TaskID, blockErr)
			}
			if blockedTask.TaskID != candidate.projection.TaskID || blockedTask.Status != domaintask.StatusBlocked {
				return queued, blocked, fmt.Errorf("canonical task %s did not return blocked outcome during interrupted run recovery", candidate.projection.TaskID)
			}
			closedRuns, listErr := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: candidate.projection.TaskID})
			if listErr != nil {
				return queued, blocked, fmt.Errorf("re-list canonical run %s after blocking task %s: %w", candidate.projection.RunID, candidate.projection.TaskID, listErr)
			}
			closedRun, exactErr := exactInterruptedRun(closedRuns, candidate.projection.TaskID, candidate.projection.RunID)
			if exactErr != nil {
				return queued, blocked, exactErr
			}
			if closedRun.Status != domaintask.RunStatusBlocked || closedRun.CompletedAt == nil {
				return queued, blocked, fmt.Errorf("canonical run %s did not close as blocked after task %s was blocked", candidate.projection.RunID, candidate.projection.TaskID)
			}
			if err := repairAgentRunProjection(ctx, store, candidate.projection, "blocked", canonicalRunSummary(closedRun), canonicalRunCompletedAt(closedRun)); err != nil {
				return queued, blocked, err
			}
			blocked++
			continue
		}

		queueID := interruptedRunResumeQueueID(candidate.projection)
		if _, ok := queueByID[queueID]; ok {
			// An existing intent, including a stale/terminal receipt, is already
			// idempotent for this Task+source Run+checkpoint. Never use it to close or rewrite
			// the historical RunID projection.
			continue
		}
		item := processRestartResumeQueueItem(candidate.projection, now)
		if err := store.SaveRunQueueItem(ctx, item); err != nil {
			return queued, blocked, err
		}
		queueByID[queueID] = item
		queued++
	}
	return queued, blocked, nil
}

func interruptedRunResumeQueueID(projection domainsuperagent.AgentRun) string {
	return fmt.Sprintf("resume:%s:%s:%d", projection.TaskID, projection.RunID, projection.CheckpointRevision)
}

func isWaitingCheckpointResumeCandidate(candidate interruptedRunRecoveryCandidate) bool {
	return candidate.run.Status == domaintask.RunStatusWaiting && candidate.task.Status == domaintask.StatusQueued && hasDurableCheckpoint(candidate.projection)
}

func isProcessRestartResumeCandidate(candidate interruptedRunRecoveryCandidate) bool {
	return candidate.run.Status == domaintask.RunStatusRunning && candidate.task.Status == domaintask.StatusRunning && candidate.projection.Status == "running" && hasDurableCheckpoint(candidate.projection)
}

func checkpointResumeQueueItem(projection domainsuperagent.AgentRun, now time.Time) domainsuperagent.RunQueueItem {
	return recoveryQueueItem(projection, domaintask.RunStartReasonCheckpointResume, now)
}

func processRestartResumeQueueItem(projection domainsuperagent.AgentRun, now time.Time) domainsuperagent.RunQueueItem {
	return recoveryQueueItem(projection, domaintask.RunStartReasonProcessRestartResume, now)
}

func recoveryQueueItem(projection domainsuperagent.AgentRun, reason domaintask.RunStartReason, now time.Time) domainsuperagent.RunQueueItem {
	queueID := interruptedRunResumeQueueID(projection)
	return domainsuperagent.RunQueueItem{
		QueueID: queueID, TaskID: projection.TaskID, RunStartReason: reason,
		WorkstreamID: projection.WorkstreamID, Goal: projection.Goal, Action: "resume", Status: "queued",
		CheckpointRevision: projection.CheckpointRevision, CheckpointSummary: projection.CheckpointSummary,
		NextAction: projection.NextAction, IdempotencyKey: queueID, CreatedAt: now.UTC(),
	}
}

func validateCheckpointResumeQueueIntent(existing, expected domainsuperagent.RunQueueItem) error {
	if existing.QueueID != expected.QueueID || existing.TaskID != expected.TaskID || existing.RunID != "" || existing.RunStartReason != expected.RunStartReason ||
		existing.WorkstreamID != expected.WorkstreamID || existing.Goal != expected.Goal || existing.Action != expected.Action || existing.Status != expected.Status ||
		existing.CheckpointRevision != expected.CheckpointRevision || existing.CheckpointSummary != expected.CheckpointSummary || existing.NextAction != expected.NextAction ||
		existing.IdempotencyKey != expected.IdempotencyKey {
		return fmt.Errorf("recovery queue intent mismatch for %s", expected.QueueID)
	}
	return nil
}

func exactInterruptedRun(runs []domaintask.Run, taskID modulecore.TaskID, runID modulecore.RunID) (domaintask.Run, error) {
	var exact domaintask.Run
	found := false
	for _, run := range runs {
		if run.RunID != runID {
			continue
		}
		if run.TaskID != taskID {
			return domaintask.Run{}, fmt.Errorf("canonical run identity mismatch: run %s belongs to task %s, want %s", runID, run.TaskID, taskID)
		}
		if found {
			return domaintask.Run{}, fmt.Errorf("canonical task %s has duplicate run %s", taskID, runID)
		}
		exact, found = run, true
	}
	if !found {
		return domaintask.Run{}, fmt.Errorf("canonical run %s for task %s is unavailable", runID, taskID)
	}
	if err := exact.Validate(); err != nil {
		return domaintask.Run{}, fmt.Errorf("canonical run %s for task %s is invalid: %w", runID, taskID, err)
	}
	return exact, nil
}

func validateInterruptedRunState(task domaintask.Task, run domaintask.Run) error {
	// A Task can be reopened or explicitly rerun after this Run was closed.
	// Therefore terminal Run history is authoritative for its projection but
	// must not be compared with the Task's later current status. Only an exact
	// active Run may authorize restart queue recovery, and it must belong to a
	// currently running Task.
	if run.Status == domaintask.RunStatusRunning && task.Status != domaintask.StatusRunning {
		return fmt.Errorf("canonical task/run state conflict for task %s: task=%s run=%s", task.TaskID, task.Status, run.Status)
	}
	return nil
}

func hasDurableCheckpoint(run domainsuperagent.AgentRun) bool {
	return run.ResumePolicy == "checkpoint" && run.CheckpointRevision > 0 &&
		strings.TrimSpace(run.CheckpointSummary) != "" && strings.TrimSpace(run.NextAction) != "" &&
		!run.LastCheckpointAt.IsZero()
}

func agentRunStatusForCanonicalRun(status domaintask.RunStatus) (string, bool) {
	switch status {
	case domaintask.RunStatusSucceeded:
		return "completed", true
	case domaintask.RunStatusWaiting:
		return "paused", true
	case domaintask.RunStatusFailed:
		return "failed", true
	case domaintask.RunStatusCancelled:
		return "cancelled", true
	case domaintask.RunStatusBlocked:
		return "blocked", true
	case domaintask.RunStatusInterrupted:
		return string(domaintask.RunStatusInterrupted), true
	case domaintask.RunStatusReassigned:
		return string(domaintask.RunStatusReassigned), true
	case domaintask.RunStatusSuperseded:
		return string(domaintask.RunStatusSuperseded), true
	default:
		return "", false
	}
}

func canonicalRunSummary(run domaintask.Run) string {
	return run.Summary
}

func canonicalRunCompletedAt(run domaintask.Run) time.Time {
	if run.CompletedAt == nil {
		return time.Time{}
	}
	return *run.CompletedAt
}

func repairAgentRunProjection(ctx context.Context, store InterruptedRunRecoveryStore, projection domainsuperagent.AgentRun, status, summary string, completedAt time.Time) error {
	if completedAt.IsZero() {
		return fmt.Errorf("canonical run %s has no completed_at for projection status %s", projection.RunID, status)
	}
	if projection.Status == status && projection.Summary == summary && projection.CompletedAt.Equal(completedAt) {
		return nil
	}
	projection.Status = status
	projection.Summary = summary
	projection.CompletedAt = completedAt
	return store.SaveAgentRun(ctx, projection)
}
