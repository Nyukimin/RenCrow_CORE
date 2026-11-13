package superagent

import (
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrivateTextRecoveryAcceptsAdditionalPrivateChatActor(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	task := domaintask.Task{
		TaskID: taskID, OwnerID: "ren", Title: "private CHAT checkpoint", Assignee: "aka",
		Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal, Route: domaintask.RouteCHAT,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
	checkpoint := domaintask.PrivateTextCheckpoint{
		TaskID: taskID, RunID: modulecore.NewRunID(), QueueItemID: modulecore.NewQueueItemID(), ActorID: "aka",
	}

	if err := validatePrivateTextRecoveryTask(task, checkpoint); err != nil {
		t.Fatalf("validatePrivateTextRecoveryTask(Aka): %v, want the private CHAT cursor eligible for recovery", err)
	}
	item := domainsuperagent.RunQueueItem{
		QueueItemID: checkpoint.QueueItemID, TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: now,
	}
	if err := validatePrivateTextRecoveryQueueItem(item, task, checkpoint); err != nil {
		t.Fatalf("validatePrivateTextRecoveryQueueItem(Aka): %v, want exact persisted private CHAT row accepted", err)
	}
}
