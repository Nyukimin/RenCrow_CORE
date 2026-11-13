package superagent

import (
	"context"
	"fmt"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const privateTextRestartRecoveryTaskLimit = 5000
const privateTextRestartRecoveryQueueLimit = 10000

type privateTextRestartTaskOwner interface {
	RunQueuePrivateTextTaskOwner
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
	ListPrivateTextRecoveryTasks(context.Context, domaintask.Status, int) ([]domaintask.Task, error)
	GetRun(context.Context, modulecore.RunID) (domaintask.Run, error)
	RecoverRunAfterProcessRestart(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status, string, string) (domaintask.Task, bool, error)
}

type PrivateTextRestartQueueStore interface {
	RunQueuePrivateTextIntentStore
	RunQueueLeaseStore
	GetRunQueueItem(context.Context, modulecore.QueueItemID) (domainsuperagent.RunQueueItem, bool, error)
	GetRunQueueItems(context.Context, []modulecore.QueueItemID) (map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, error)
	ListActiveRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error)
}

type PrivateTextRestartRecoveryResult struct {
	RecoveredRuns    int
	QueuedResumes    int
	ClosedReceipts   int
	FinalReceipts    int
	DeferredReceipts int
}

// RecoverPrivateTextRunQueueReceipts repairs only Tasks whose Task-owned
// private cursor names the exact Task, Run, queue item, and actor. A CORE
// crash after provider completion but before SavePrivateTextOutput can repeat
// remote compute; after that output is durable, the resumed runner skips the
// provider call.
func RecoverPrivateTextRunQueueReceipts(ctx context.Context, store PrivateTextRestartQueueStore, owner privateTextRestartTaskOwner, now time.Time) (PrivateTextRestartRecoveryResult, error) {
	var result PrivateTextRestartRecoveryResult
	if ctx == nil || store == nil || owner == nil {
		return result, fmt.Errorf("private text restart recovery dependencies are unavailable")
	}
	if now.IsZero() {
		return result, fmt.Errorf("private text restart recovery time is required")
	}
	now = now.UTC()

	activeItems, err := store.ListActiveRunQueueItems(ctx, privateTextRestartRecoveryQueueLimit+1)
	if err != nil {
		return result, fmt.Errorf("list bounded active private text queue receipts: %w", err)
	}
	if len(activeItems) > privateTextRestartRecoveryQueueLimit {
		return result, fmt.Errorf("private text active queue receipt limit exceeded")
	}
	activeByID := make(map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, len(activeItems))
	privateActiveTasks := make(map[modulecore.TaskID]struct{})
	for _, item := range activeItems {
		if _, duplicate := activeByID[item.QueueItemID]; duplicate {
			return result, fmt.Errorf("private text recovery found duplicate queue item %s", item.QueueItemID)
		}
		activeByID[item.QueueItemID] = item
		if isPrivateTextQueueItem(item) {
			privateActiveTasks[item.TaskID] = struct{}{}
		}
	}

	tasksByID := make(map[modulecore.TaskID]domaintask.Task)
	for _, status := range []domaintask.Status{domaintask.StatusRunning, domaintask.StatusWaiting} {
		tasks, err := owner.ListPrivateTextRecoveryTasks(ctx, status, privateTextRestartRecoveryTaskLimit+1)
		if err != nil {
			return result, fmt.Errorf("list bounded %s Tasks for private text recovery: %w", status, err)
		}
		if len(tasks) > privateTextRestartRecoveryTaskLimit {
			return result, fmt.Errorf("private text %s Task recovery limit exceeded", status)
		}
		for _, task := range tasks {
			if task.Status != status {
				continue
			}
			if prior, duplicate := tasksByID[task.TaskID]; duplicate && prior.Status != task.Status {
				return result, fmt.Errorf("private text recovery observed changing Task status for %s", task.TaskID)
			}
			tasksByID[task.TaskID] = task
		}
	}

	tasksByID, checkpointsByTask, queueIDs, err := readPrivateTextRecoveryCandidates(ctx, owner, tasksByID, privateActiveTasks)
	if err != nil {
		return result, err
	}
	if len(queueIDs) > 0 {
		queueItems, err := store.GetRunQueueItems(ctx, queueIDs)
		if err != nil {
			return result, fmt.Errorf("read exact private text source queue receipts: %w", err)
		}
		for _, task := range tasksByID {
			checkpoint, found := checkpointsByTask[task.TaskID]
			if !found {
				continue
			}
			queueItem, exists := queueItems[checkpoint.QueueItemID]
			if !exists {
				return result, fmt.Errorf("private text cursor for Task %s has no exact queue receipt %s", task.TaskID, checkpoint.QueueItemID)
			}
			if err := reconcilePrivateTextActiveTask(ctx, store, owner, task, checkpoint, queueItem, now, &result); err != nil {
				return result, err
			}
		}
	}
	for _, queueItem := range activeItems {
		if !isPrivateTextQueueItem(queueItem) || queueItem.Status != "claimed" && queueItem.Status != "reserved" {
			continue
		}
		task, err := owner.Get(ctx, queueItem.TaskID)
		if err != nil {
			return result, fmt.Errorf("get Task for unfinished private text receipt %s: %w", queueItem.QueueItemID, err)
		}
		if task.Status != domaintask.StatusSucceeded {
			continue
		}
		if queueItem.RunID == "" {
			// Older first-chat receipts may predate Task-owned cursor issuance.
			// They are never replayed blindly, and an unattached legacy row has no
			// exact Run that can authorize a repair.
			result.DeferredReceipts++
			continue
		}
		run, err := owner.GetRun(ctx, queueItem.RunID)
		if err != nil {
			return result, fmt.Errorf("get Run for unfinished private text receipt %s: %w", queueItem.QueueItemID, err)
		}
		if run.TaskID != task.TaskID || run.Validate() != nil || run.CompletedAt == nil ||
			(run.Status != domaintask.RunStatusSucceeded && run.Status != domaintask.RunStatusWaiting) {
			continue
		}
		checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
		if err != nil {
			// A later generic rerun/reassignment can make an old private cursor
			// unreadable. The exact old Run above is terminal, so leave
			// that unrelated receipt untouched rather than failing CORE startup.
			result.DeferredReceipts++
			continue
		}
		if !found {
			// Preserve the legacy first-chat crash window where the Task and Run
			// succeeded before private cursors existed. Close only when Task and
			// Run have the identical terminal timestamp and exact first receipt.
			if run.Status == domaintask.RunStatusSucceeded && queueItem.IdempotencyKey == domainsuperagent.RunQueueIntentPrivateFirstChat &&
				task.FinishedAt != nil && task.FinishedAt.Equal(*run.CompletedAt) {
				if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "completed", privateTextQueueProjectionSummary, now); err != nil {
					return result, err
				}
				result.FinalReceipts++
			} else {
				result.DeferredReceipts++
			}
			continue
		}
		if checkpoint.TaskID != task.TaskID || checkpoint.QueueItemID != queueItem.QueueItemID || checkpoint.RunID != queueItem.RunID || checkpoint.ActorID != task.Assignee || checkpoint.Output == "" {
			reconciled, err := reconcileSupersededPrivateTextReceipt(ctx, store, owner, task, checkpoint, queueItem, now)
			if err != nil {
				return result, err
			}
			if reconciled {
				result.ClosedReceipts++
			} else {
				result.DeferredReceipts++
			}
			continue
		}
		if run.Status != domaintask.RunStatusSucceeded {
			result.DeferredReceipts++
			continue
		}
		if err := validatePrivateTextRecoveryTask(task, checkpoint); err != nil {
			return result, err
		}
		if err := validatePrivateTextRecoveryQueueItem(queueItem, task, checkpoint); err != nil {
			return result, err
		}
		if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "completed", privateTextQueueProjectionSummary, now); err != nil {
			return result, err
		}
		result.FinalReceipts++
	}
	return result, nil
}

func reconcileSupersededPrivateTextReceipt(ctx context.Context, store PrivateTextRestartQueueStore, owner privateTextRestartTaskOwner, task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint, oldReceipt domainsuperagent.RunQueueItem, now time.Time) (bool, error) {
	if oldReceipt.RunID == "" || checkpoint.RunID == oldReceipt.RunID {
		return false, nil
	}
	oldRun, err := owner.GetRun(ctx, oldReceipt.RunID)
	if err != nil {
		return false, fmt.Errorf("get exact predecessor Run for receipt %s: %w", oldReceipt.QueueItemID, err)
	}
	if oldRun.TaskID != task.TaskID || oldRun.Status != domaintask.RunStatusWaiting || oldRun.CompletedAt == nil || oldRun.Validate() != nil {
		return false, nil
	}
	currentReceipt, found, err := store.GetRunQueueItem(ctx, checkpoint.QueueItemID)
	if err != nil {
		return false, fmt.Errorf("get exact current private text receipt %s: %w", checkpoint.QueueItemID, err)
	}
	if !found || !domainsuperagent.IsPrivateTextResumeQueueItem(currentReceipt) || currentReceipt.TaskID != task.TaskID ||
		currentReceipt.PredecessorRunID != oldRun.RunID || currentReceipt.Status == "failed" || currentReceipt.Status == "blocked" {
		return false, nil
	}
	currentRun, err := owner.GetRun(ctx, checkpoint.RunID)
	if err != nil {
		return false, fmt.Errorf("get exact current private text Run %s: %w", checkpoint.RunID, err)
	}
	if currentRun.TaskID != task.TaskID || currentRun.Status != domaintask.RunStatusSucceeded || currentRun.StartReason != domaintask.RunStartReasonCheckpointResume ||
		currentRun.StartCheckpointSHA256 != currentReceipt.CheckpointDigest || currentRun.Validate() != nil || checkpoint.Output == "" {
		return false, nil
	}
	if err := closePrivateTextSourceReceipt(ctx, store, oldReceipt, "cancelled", privateTextPausedQueueReason, now); err != nil {
		return false, err
	}
	return true, nil
}

func readPrivateTextRecoveryCandidates(ctx context.Context, owner privateTextRestartTaskOwner, tasksByID map[modulecore.TaskID]domaintask.Task, privateActiveTasks map[modulecore.TaskID]struct{}) (map[modulecore.TaskID]domaintask.Task, map[modulecore.TaskID]domaintask.PrivateTextCheckpoint, []modulecore.QueueItemID, error) {
	checkpoints := make(map[modulecore.TaskID]domaintask.PrivateTextCheckpoint, len(tasksByID))
	queueIDs := make([]modulecore.QueueItemID, 0, len(tasksByID))
	for _, task := range tasksByID {
		_, hasActivePrivateReceipt := privateActiveTasks[task.TaskID]
		if task.Route != domaintask.RouteCHAT || task.Assignee == "" ||
			(task.Status == domaintask.StatusWaiting && task.WaitingReason != domaintask.PrivateTextWaitingReason) {
			continue
		}
		checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
		if err != nil {
			if !hasActivePrivateReceipt {
				continue
			}
			return tasksByID, checkpoints, queueIDs, fmt.Errorf("read private text cursor for Task %s: %w", task.TaskID, err)
		}
		if !found {
			continue
		}
		if err := checkpoint.Validate(); err != nil || checkpoint.TaskID != task.TaskID || checkpoint.ActorID != task.Assignee {
			if !hasActivePrivateReceipt {
				continue
			}
			return tasksByID, checkpoints, queueIDs, fmt.Errorf("private text cursor for Task %s has invalid identity", task.TaskID)
		}
		if err := validatePrivateTextRecoveryTask(task, checkpoint); err != nil {
			if !hasActivePrivateReceipt {
				continue
			}
			return tasksByID, checkpoints, queueIDs, err
		}
		checkpoints[task.TaskID] = checkpoint
		queueIDs = append(queueIDs, checkpoint.QueueItemID)
	}
	return tasksByID, checkpoints, queueIDs, nil
}

func reconcilePrivateTextActiveTask(ctx context.Context, store PrivateTextRestartQueueStore, owner privateTextRestartTaskOwner, task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint, queueItem domainsuperagent.RunQueueItem, now time.Time, result *PrivateTextRestartRecoveryResult) error {
	if err := validatePrivateTextRecoveryQueueItem(queueItem, task, checkpoint); err != nil {
		return err
	}
	run, err := owner.GetRun(ctx, checkpoint.RunID)
	if err != nil {
		return fmt.Errorf("get exact private text Run %s: %w", checkpoint.RunID, err)
	}
	if run.TaskID != task.TaskID || run.Validate() != nil {
		return fmt.Errorf("private text cursor Run %s does not match Task %s", checkpoint.RunID, task.TaskID)
	}
	if domainsuperagent.IsPrivateTextResumeQueueItem(queueItem) {
		if run.StartReason != domaintask.RunStartReasonCheckpointResume || run.StartCheckpointSHA256 != queueItem.CheckpointDigest {
			return fmt.Errorf("private text Run %s does not retain its exact predecessor digest", checkpoint.RunID)
		}
		predecessor, err := owner.GetRun(ctx, queueItem.PredecessorRunID)
		if err != nil {
			return fmt.Errorf("get exact private text predecessor Run %s: %w", queueItem.PredecessorRunID, err)
		}
		if predecessor.TaskID != task.TaskID || predecessor.Status != domaintask.RunStatusWaiting || predecessor.CompletedAt == nil || predecessor.Validate() != nil {
			return fmt.Errorf("private text Run %s predecessor does not match its exact waiting Task Run", checkpoint.RunID)
		}
	}

	switch task.Status {
	case domaintask.StatusRunning:
		if run.Status != domaintask.RunStatusRunning {
			return fmt.Errorf("private text running Task %s does not have an exact active Run", task.TaskID)
		}
		closeSource := false
		switch queueItem.Status {
		case "reserved":
			if queueItem.RunID == "" && queueItem.LeaseToken != "" {
				attached, attachErr := store.AttachRunQueueRun(ctx, string(queueItem.QueueItemID), queueItem.LeaseToken, checkpoint.RunID)
				if attachErr != nil {
					return fmt.Errorf("attach private text Run %s to its reserved receipt: %w", checkpoint.RunID, attachErr)
				}
				if attached {
					queueItem.Status, queueItem.RunID = "claimed", checkpoint.RunID
					closeSource = true
				} else {
					result.DeferredReceipts++
				}
			} else if queueItem.RunID == checkpoint.RunID {
				closeSource = true
			} else {
				result.DeferredReceipts++
			}
		case "claimed":
			if queueItem.RunID == "" || queueItem.RunID == checkpoint.RunID {
				closeSource = true
			} else {
				result.DeferredReceipts++
			}
		case "queued":
			result.DeferredReceipts++
		case "cancelled", "completed", "failed", "blocked":
			// A lease may have changed after Run issuance. The canonical cursor
			// remains authoritative; recover the Run and successor without
			// rewriting a receipt that no longer carries this process's lease.
			if queueItem.RunID != "" && queueItem.RunID != checkpoint.RunID {
				result.DeferredReceipts++
			}
		default:
			return fmt.Errorf("private text running Task %s has source receipt status %q", task.TaskID, queueItem.Status)
		}
		recoveredTask, recovered, err := owner.RecoverRunAfterProcessRestart(ctx, task.TaskID, checkpoint.RunID, task.Assignee,
			domaintask.StatusWaiting, "", domaintask.PrivateTextWaitingReason)
		if err != nil {
			return fmt.Errorf("recover old-generation private text Run %s: %w", checkpoint.RunID, err)
		}
		if recovered {
			result.RecoveredRuns++
		} else if recoveredTask.Status != domaintask.StatusWaiting || recoveredTask.WaitingReason != domaintask.PrivateTextWaitingReason {
			return fmt.Errorf("private text Run %s was not recovered to its fixed waiting state", checkpoint.RunID)
		}
		if err := ensurePrivateTextResumeReceipt(ctx, store, task, checkpoint, now, result); err != nil {
			return err
		}
		if closeSource {
			if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "cancelled", privateTextPausedQueueReason, now); err != nil {
				return err
			}
			result.ClosedReceipts++
		}
	case domaintask.StatusWaiting:
		if run.Status != domaintask.RunStatusWaiting || run.CompletedAt == nil || task.WaitingReason != domaintask.PrivateTextWaitingReason {
			return fmt.Errorf("private text waiting Task %s does not match its exact waiting Run", task.TaskID)
		}
		if err := ensurePrivateTextResumeReceipt(ctx, store, task, checkpoint, now, result); err != nil {
			return err
		}
		switch queueItem.Status {
		case "reserved":
			if queueItem.RunID == "" && queueItem.LeaseToken != "" {
				attached, attachErr := store.AttachRunQueueRun(ctx, string(queueItem.QueueItemID), queueItem.LeaseToken, checkpoint.RunID)
				if attachErr != nil {
					return fmt.Errorf("attach private text waiting Run %s to its reserved receipt: %w", checkpoint.RunID, attachErr)
				}
				if attached {
					queueItem.Status, queueItem.RunID = "claimed", checkpoint.RunID
					if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "cancelled", privateTextPausedQueueReason, now); err != nil {
						return err
					}
					result.ClosedReceipts++
				} else {
					result.DeferredReceipts++
				}
			} else if queueItem.RunID == checkpoint.RunID && queueItem.LeaseToken != "" {
				if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "cancelled", privateTextPausedQueueReason, now); err != nil {
					return err
				}
				result.ClosedReceipts++
			} else {
				result.DeferredReceipts++
			}
		case "claimed":
			if (queueItem.RunID == "" || queueItem.RunID == checkpoint.RunID) && queueItem.LeaseToken != "" {
				if err := closePrivateTextSourceReceipt(ctx, store, queueItem, "cancelled", privateTextPausedQueueReason, now); err != nil {
					return err
				}
				result.ClosedReceipts++
			} else {
				result.DeferredReceipts++
			}
		case "cancelled":
			if queueItem.RunID != "" && queueItem.RunID != checkpoint.RunID {
				result.DeferredReceipts++
			}
		case "queued":
			result.DeferredReceipts++
		case "completed", "failed", "blocked":
			if queueItem.RunID != "" && queueItem.RunID != checkpoint.RunID {
				result.DeferredReceipts++
			}
			result.DeferredReceipts++
		default:
			return fmt.Errorf("private text waiting Task %s has source receipt status %q", task.TaskID, queueItem.Status)
		}
	default:
		return fmt.Errorf("private text recovery encountered unsupported Task status %q", task.Status)
	}
	return nil
}

func validatePrivateTextRecoveryTask(task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint) error {
	if err := task.Validate(); err != nil || task.TaskID != checkpoint.TaskID || task.Route != domaintask.RouteCHAT ||
		task.Assignee != checkpoint.ActorID || !domainsuperagent.IsPrivateChatQueueActorID(task.Assignee) ||
		(domainsuperagent.ValidateActorID(task.Assignee) != nil && !domainsuperagent.IsPrivateChatQueueTask(task)) {
		return fmt.Errorf("private text restart Task %s is outside its canonical CHAT actor scope", task.TaskID)
	}
	return nil
}

func validatePrivateTextRecoveryQueueItem(item domainsuperagent.RunQueueItem, task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return fmt.Errorf("private text cursor queue receipt is invalid: %w", err)
	}
	if item.TaskID != task.TaskID || item.QueueItemID != checkpoint.QueueItemID ||
		item.Goal != domainsuperagent.RunQueueGoalPrivateFirstChat || item.Action != "chat" ||
		(item.IdempotencyKey != domainsuperagent.RunQueueIntentPrivateFirstChat && !domainsuperagent.IsPrivateTextResumeQueueItem(item)) {
		return fmt.Errorf("private text cursor and queue receipt identity do not match Task %s", task.TaskID)
	}
	if item.IdempotencyKey == domainsuperagent.RunQueueIntentPrivateFirstChat {
		if item.RunStartReason != domaintask.RunStartReasonFirst || checkpoint.RunID == "" {
			return fmt.Errorf("private text first-run receipt does not match Task %s", task.TaskID)
		}
		return nil
	}
	if item.RunStartReason != domaintask.RunStartReasonCheckpointResume || item.PredecessorRunID == "" || item.CheckpointDigest == "" {
		return fmt.Errorf("private text resume receipt does not match Task %s", task.TaskID)
	}
	return nil
}

func attachPrivateTextRecoveryRun(ctx context.Context, store PrivateTextRestartQueueStore, item domainsuperagent.RunQueueItem, runID modulecore.RunID) (domainsuperagent.RunQueueItem, error) {
	if item.Status == "claimed" {
		if item.RunID != runID {
			return item, fmt.Errorf("private text claimed receipt %s belongs to another Run", item.QueueItemID)
		}
		return item, nil
	}
	if item.Status != "reserved" || item.RunID != "" || item.LeaseToken == "" {
		return item, fmt.Errorf("private text issuance receipt %s is not an exact reserved lease", item.QueueItemID)
	}
	attached, err := store.AttachRunQueueRun(ctx, string(item.QueueItemID), item.LeaseToken, runID)
	if err != nil {
		return item, fmt.Errorf("attach private text Run %s to its exact reserved receipt: %w", runID, err)
	}
	if !attached {
		return item, fmt.Errorf("private text queue lease for %s changed before Run attachment", item.QueueItemID)
	}
	item.Status, item.RunID = "claimed", runID
	return item, nil
}

func ensurePrivateTextResumeReceipt(ctx context.Context, store PrivateTextRestartQueueStore, task domaintask.Task, checkpoint domaintask.PrivateTextCheckpoint, now time.Time, result *PrivateTextRestartRecoveryResult) error {
	next := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID,
		PredecessorRunID: checkpoint.RunID, CheckpointDigest: checkpoint.Digest(),
		RunStartReason: domaintask.RunStartReasonCheckpointResume, WorkstreamID: string(task.WorkstreamID),
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		Priority: taskPriorityRank(task.Priority), IdempotencyKey: domainsuperagent.PrivateTextResumeQueueKey(checkpoint.RunID),
		CreatedAt: task.CreatedAt,
	}
	if err := domainsuperagent.ValidateRunQueueItem(next); err != nil {
		return fmt.Errorf("validate private text restart successor: %w", err)
	}
	stored, inserted, err := store.InsertRunQueueItemIfAbsent(ctx, next)
	if err != nil {
		return fmt.Errorf("insert private text restart successor: %w", err)
	}
	if !privateTextResumeIntentReceiptMatches(stored, next) || stored.Status != "queued" && stored.Status != "reserved" && stored.Status != "claimed" {
		return fmt.Errorf("private text restart successor receipt does not match the exact cursor")
	}
	if inserted {
		result.QueuedResumes++
	}
	return nil
}

func closePrivateTextSourceReceipt(ctx context.Context, store PrivateTextRestartQueueStore, item domainsuperagent.RunQueueItem, status, reason string, now time.Time) error {
	if item.LeaseToken == "" {
		return fmt.Errorf("private text receipt %s has no exact lease token", item.QueueItemID)
	}
	completed, err := store.CompleteRunQueueItem(ctx, string(item.QueueItemID), item.LeaseToken, status, reason, now)
	if err != nil {
		return fmt.Errorf("complete private text receipt %s with its lease CAS: %w", item.QueueItemID, err)
	}
	if !completed {
		return fmt.Errorf("private text receipt %s lease changed before restart reconciliation", item.QueueItemID)
	}
	return nil
}
