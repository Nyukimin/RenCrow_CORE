package superagent

import (
	"context"
	"os"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreSavesAndListsSuperAgentRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := store.SaveAgentRun(context.Background(), domainsuperagent.AgentRun{
		RunID:     runID,
		TaskID:    taskID,
		ActorID:   "mio",
		Status:    "running",
		StartedAt: now,
	}); err != nil {
		t.Fatalf("SaveAgentRun() error = %v", err)
	}
	if err := store.SaveSubagentTask(context.Background(), domainsuperagent.SubagentTask{
		TaskID:               taskID,
		RunID:                runID,
		ActorID:              "shiro",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "pending",
		CreatedAt:            now,
	}); err != nil {
		t.Fatalf("SaveSubagentTask() error = %v", err)
	}
	if err := store.SaveSubagentTask(context.Background(), domainsuperagent.SubagentTask{
		TaskID:               taskID,
		RunID:                runID,
		ActorID:              "shiro",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "completed",
		CreatedAt:            now,
		CompletedAt:          now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveSubagentTask(completed) error = %v", err)
	}
	if err := store.SaveContextPack(context.Background(), domainsuperagent.ContextPack{
		ContextPackID: "ctx_1",
		TaskID:        taskID,
		RunID:         runID,
		Summary:       "summary",
		TokenEstimate: 1200,
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("SaveContextPack() error = %v", err)
	}
	if err := store.SaveRunQueueItem(context.Background(), domainsuperagent.RunQueueItem{
		QueueID:        "queue_1",
		TaskID:         taskID,
		RunStartReason: domaintask.RunStartReasonCheckpointResume,
		Goal:           "resume run",
		Action:         "resume",
		Status:         "queued",
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("SaveRunQueueItem() error = %v", err)
	}
	runs, err := store.ListAgentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListAgentRuns() = %#v, %v", runs, err)
	}
	tasks, err := store.ListSubagentTasks(context.Background(), 10)
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != taskID || tasks[0].Status != "completed" {
		t.Fatalf("ListSubagentTasks() = %#v, %v", tasks, err)
	}
	contexts, err := store.ListContextPacks(context.Background(), 10)
	if err != nil || len(contexts) != 1 {
		t.Fatalf("ListContextPacks() = %#v, %v", contexts, err)
	}
	queue, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(queue) != 1 {
		t.Fatalf("ListRunQueueItems() = %#v, %v", queue, err)
	}
}

func TestJSONLStoreListSubagentTasksReturnsLatestStatePerTask(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 9, 40, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	running := domainsuperagent.SubagentTask{
		TaskID:               taskID,
		RunID:                runID,
		ActorID:              "midori",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "running",
		CreatedAt:            now,
	}
	completed := running
	completed.Status = "completed"
	completed.CompletedAt = now.Add(time.Second)
	if err := store.SaveSubagentTask(ctx, running); err != nil {
		t.Fatalf("SaveSubagentTask(running) error = %v", err)
	}
	if err := store.SaveSubagentTask(ctx, completed); err != nil {
		t.Fatalf("SaveSubagentTask(completed) error = %v", err)
	}

	tasks, err := store.ListSubagentTasks(ctx, 10)
	if err != nil {
		t.Fatalf("ListSubagentTasks() error = %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskID != taskID || tasks[0].Status != "completed" || tasks[0].CompletedAt.IsZero() {
		t.Fatalf("tasks=%#v", tasks)
	}
}

func TestJSONLStoreRejectsOversizedContextPack(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 100)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	err := store.SaveContextPack(context.Background(), domainsuperagent.ContextPack{
		ContextPackID: "ctx_1",
		TaskID:        taskID,
		RunID:         runID,
		Summary:       "summary",
		TokenEstimate: 101,
		CreatedAt:     time.Now(),
	})
	if err == nil {
		t.Fatal("expected oversized context pack to fail")
	}
}

func TestJSONLStoreListRunQueueItemsReturnsLatestStatePerQueue(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	now := time.Date(2026, 5, 19, 8, 40, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	for _, status := range []string{"queued", "claimed", "completed"} {
		item := domainsuperagent.RunQueueItem{
			QueueID:        "queue_1",
			TaskID:         taskID,
			RunStartReason: domaintask.RunStartReasonCheckpointResume,
			Goal:           "resume run",
			Action:         "resume",
			Status:         status,
			CreatedAt:      now,
		}
		if status == "claimed" || status == "completed" {
			item.RunID = runID
		}
		if status == "claimed" {
			item.ClaimedAt = now.Add(time.Second)
			item.LeaseToken = "owner-1"
			item.LeaseUntil = now.Add(time.Minute)
		}
		if status == "completed" {
			item.CompletedAt = now.Add(2 * time.Second)
		}
		if err := store.SaveRunQueueItem(context.Background(), item); err != nil {
			t.Fatalf("SaveRunQueueItem(%s) error = %v", status, err)
		}
	}

	queue, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRunQueueItems() error = %v", err)
	}
	if len(queue) != 1 || queue[0].QueueID != "queue_1" || queue[0].Status != "completed" {
		t.Fatalf("queue=%#v", queue)
	}
}

func TestJSONLRunQueueClaimReservesAndReleasesExpiredRun(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	first := NewJSONLStore(root, 3000)
	taskID := modulecore.NewTaskID()
	item := domainsuperagent.RunQueueItem{QueueID: "resume:task-1:7", TaskID: taskID, RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "continue", Action: "resume", Status: "queued", CheckpointRevision: 7, CreatedAt: now}
	if err := first.SaveRunQueueItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, err := first.ClaimNextRunQueueItem(context.Background(), now, now.Add(time.Minute), "owner-1")
	if err != nil || claimed == nil || claimed.Status != "reserved" || claimed.RunID != "" || claimed.AttemptCount != 1 {
		t.Fatalf("first claim=%#v err=%v", claimed, err)
	}
	if renewed, err := first.RenewRunQueueLease(context.Background(), claimed.QueueID, "wrong-owner", now.Add(30*time.Second)); err != nil || renewed {
		t.Fatalf("reservation renewed with wrong token=%v err=%v", renewed, err)
	}
	if renewed, err := first.RenewRunQueueLease(context.Background(), claimed.QueueID, "owner-1", now.Add(75*time.Second)); err != nil || !renewed {
		t.Fatalf("reservation renewal=%v err=%v", renewed, err)
	}
	firstRunID := modulecore.NewRunID()
	claimed.Status = "claimed"
	claimed.RunID = firstRunID
	if err := first.SaveRunQueueItem(context.Background(), *claimed); err != nil {
		t.Fatalf("persist scheduler-attached run: %v", err)
	}

	reopened := NewJSONLStore(root, 3000)
	if got, err := reopened.ClaimNextRunQueueItem(context.Background(), now.Add(30*time.Second), now.Add(90*time.Second), "owner-2"); err != nil || got != nil {
		t.Fatalf("unexpired claim=%#v err=%v", got, err)
	}
	recovered, err := reopened.ClaimNextRunQueueItem(context.Background(), now.Add(76*time.Second), now.Add(2*time.Minute), "owner-2")
	if err != nil || recovered == nil || recovered.Status != "reserved" || recovered.RunID != "" || recovered.LeaseToken != "owner-2" || recovered.AttemptCount != 2 || recovered.CheckpointRevision != 7 {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-2", "completed", "reservation must attach a run first", now.Add(77*time.Second)); err != nil || completed {
		t.Fatalf("reserved completion accepted=%v err=%v", completed, err)
	}
	secondRunID := modulecore.NewRunID()
	recovered.Status = "claimed"
	recovered.RunID = secondRunID
	if err := reopened.SaveRunQueueItem(context.Background(), *recovered); err != nil {
		t.Fatalf("persist recovered scheduler-attached run: %v", err)
	}
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-1", "completed", "stale result", now.Add(78*time.Second)); err != nil || completed {
		t.Fatalf("stale owner completion accepted=%v err=%v", completed, err)
	}
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-2", "completed", "resumed", now.Add(79*time.Second)); err != nil || !completed {
		t.Fatalf("current owner completion=%v err=%v", completed, err)
	}
	items, err := reopened.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].Status != "completed" || items[0].RunID != secondRunID {
		t.Fatalf("completed queue item=%#v err=%v", items, err)
	}
}

func TestJSONLAttachRunQueueRunRejectsStaleLeaseAndPreservesCurrentReservation(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	item := domainsuperagent.RunQueueItem{
		QueueID:        "attach:task-1",
		TaskID:         modulecore.NewTaskID(),
		RunStartReason: domaintask.RunStartReasonCheckpointResume,
		Goal:           "attach canonical run",
		Action:         "resume",
		Reason:         "preserve this reason",
		Status:         "queued",
		AttemptCount:   3,
		CreatedAt:      now,
	}
	if err := store.SaveRunQueueItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNextRunQueueItem(context.Background(), now, now.Add(time.Minute), "owner-1")
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	if attached, err := store.AttachRunQueueRun(context.Background(), first.QueueID, "owner-1", modulecore.RunID("run_legacy")); err == nil || attached {
		t.Fatalf("invalid canonical run attach=%v err=%v, want validation error", attached, err)
	}
	staleRunID := modulecore.NewRunID()
	if attached, err := store.AttachRunQueueRun(context.Background(), first.QueueID, "stale-owner", staleRunID); err != nil || attached {
		t.Fatalf("stale attach=%v err=%v, want false", attached, err)
	}
	reserved, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(reserved) != 1 || reserved[0].Status != "reserved" || reserved[0].RunID != "" || reserved[0].LeaseToken != "owner-1" {
		t.Fatalf("reservation after stale attach=%#v err=%v", reserved, err)
	}

	second, err := store.ClaimNextRunQueueItem(context.Background(), now.Add(2*time.Minute), now.Add(3*time.Minute), "owner-2")
	if err != nil || second == nil || second.LeaseToken != "owner-2" || second.AttemptCount != 5 {
		t.Fatalf("reacquired reservation=%#v err=%v", second, err)
	}
	if attached, err := store.AttachRunQueueRun(context.Background(), second.QueueID, "owner-1", staleRunID); err != nil || attached {
		t.Fatalf("old owner attach after reacquire=%v err=%v, want false", attached, err)
	}
	currentRunID := modulecore.NewRunID()
	if attached, err := store.AttachRunQueueRun(context.Background(), second.QueueID, "owner-2", currentRunID); err != nil || !attached {
		t.Fatalf("current owner attach=%v err=%v, want true", attached, err)
	}
	if attached, err := store.AttachRunQueueRun(context.Background(), second.QueueID, "owner-1", staleRunID); err != nil || attached {
		t.Fatalf("stale overwrite after current attach=%v err=%v, want false", attached, err)
	}
	items, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("attached queue items=%#v err=%v", items, err)
	}
	got := items[0]
	if got.Status != "claimed" || got.RunID != currentRunID || got.LeaseToken != "owner-2" || got.TaskID != item.TaskID || got.Reason != item.Reason || got.AttemptCount != 5 {
		t.Fatalf("attached queue item=%#v", got)
	}
}

func TestJSONLStoreListAgentRunsReturnsLatestStatePerRun(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 9, 28, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	running := domainsuperagent.AgentRun{
		RunID:     runID,
		TaskID:    taskID,
		ActorID:   "mio",
		Goal:      "scheduler E2E",
		Status:    "running",
		StartedAt: now,
		Summary:   "route=CHAT",
	}
	failed := running
	failed.Status = "failed"
	failed.CompletedAt = now.Add(5 * time.Second)
	failed.Summary = "failed to execute request"
	if err := store.SaveAgentRun(ctx, running); err != nil {
		t.Fatalf("SaveAgentRun(running) error = %v", err)
	}
	if err := store.SaveAgentRun(ctx, failed); err != nil {
		t.Fatalf("SaveAgentRun(failed) error = %v", err)
	}

	runs, err := store.ListAgentRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListAgentRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != runID || runs[0].Status != "failed" || runs[0].CompletedAt.IsZero() {
		t.Fatalf("runs=%#v", runs)
	}
}

func TestJSONLStoreFindAgentRunByIDReturnsLatestExactRecord(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	running := domainsuperagent.AgentRun{RunID: runID, TaskID: taskID, ActorID: "mio", Status: "running", StartedAt: now}
	completed := running
	completed.Status = "completed"
	completed.CompletedAt = now.Add(time.Minute)
	if err := store.SaveAgentRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRun(ctx, completed); err != nil {
		t.Fatal(err)
	}
	item, found, err := store.FindAgentRunByID(ctx, string(runID))
	if err != nil || !found || item.Status != "completed" {
		t.Fatalf("item=%#v found=%v err=%v", item, found, err)
	}

	missing, found, err := store.FindAgentRunByID(ctx, "missing")
	if err != nil || found || missing.RunID != "" {
		t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
	}

	prefixStore := NewJSONLStore(t.TempDir(), 3000)
	prefixTaskID, prefixRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := prefixStore.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: prefixRunID, TaskID: prefixTaskID, ActorID: "mio", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, found, err = prefixStore.FindAgentRunByID(ctx, string(runID))
	if err != nil || found || item.RunID != "" {
		t.Fatalf("prefix match item=%#v found=%v err=%v", item, found, err)
	}

	corruptStore := NewJSONLStore(t.TempDir(), 3000)
	if err := os.WriteFile(corruptStore.agentRunPath, []byte("{\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := corruptStore.FindAgentRunByID(ctx, string(runID)); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}
