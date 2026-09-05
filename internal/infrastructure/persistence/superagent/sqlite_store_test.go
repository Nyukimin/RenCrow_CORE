package superagent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSQLiteStoreSavesAndListsSuperAgentRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := store.SaveAgentRun(context.Background(), domainsuperagent.AgentRun{
		RunID:     runID,
		TaskID:    taskID,
		AgentType: "LeadAgent",
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
	completedTask := domainsuperagent.SubagentTask{
		TaskID:               taskID,
		RunID:                runID,
		ActorID:              "shiro",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "completed",
		CreatedAt:            now,
		CompletedAt:          now.Add(time.Minute),
	}
	if err := store.SaveSubagentTask(context.Background(), completedTask); err != nil {
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
	if err := store.SaveMessageChannel(context.Background(), domainsuperagent.MessageChannel{
		ChannelID:   "ch_1",
		ChannelType: "web",
		Status:      "active",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveMessageChannel() error = %v", err)
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
	if err != nil || len(runs) != 1 || runs[0].RunID != runID {
		t.Fatalf("ListAgentRuns() = %#v, %v", runs, err)
	}
	tasks, err := store.ListSubagentTasks(context.Background(), 10)
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != taskID || tasks[0].Status != "completed" {
		t.Fatalf("ListSubagentTasks() = %#v, %v", tasks, err)
	}
	contexts, err := store.ListContextPacks(context.Background(), 10)
	if err != nil || len(contexts) != 1 || contexts[0].ContextPackID != "ctx_1" {
		t.Fatalf("ListContextPacks() = %#v, %v", contexts, err)
	}
	channels, err := store.ListMessageChannels(context.Background(), 10)
	if err != nil || len(channels) != 1 || channels[0].ChannelID != "ch_1" {
		t.Fatalf("ListMessageChannels() = %#v, %v", channels, err)
	}
	queue, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(queue) != 1 || queue[0].QueueID != "queue_1" {
		t.Fatalf("ListRunQueueItems() = %#v, %v", queue, err)
	}
}

func TestSQLiteStoreSubagentTaskSchemaUsesCanonicalTaskIdentity(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(subagent_task)`)
	if err != nil {
		t.Fatalf("inspect subagent_task schema: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan subagent_task schema: %v", err)
		}
		columns[name] = true
		if name == "task_id" && primaryKey != 1 {
			t.Fatalf("task_id primary key=%d, want 1", primaryKey)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read subagent_task schema: %v", err)
	}
	for _, name := range []string{"task_id", "run_id", "created_at", "payload"} {
		if !columns[name] {
			t.Fatalf("subagent_task missing column %q", name)
		}
	}
	for _, name := range []string{"subagent_id", "parent_run_id", "agent_type"} {
		if columns[name] {
			t.Fatalf("subagent_task contains legacy column %q", name)
		}
	}
}

func TestSQLiteStoreDoesNotCreateLegacyTraceEventTable(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='trace_event'`).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if count != 0 {
		t.Fatal("legacy trace_event table was created")
	}
}

func TestSQLiteRunQueueClaimSurvivesReopenAndRecoversAfterLeaseExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	first, err := NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
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
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-1", "completed", "stale result", now.Add(62*time.Second)); err != nil || completed {
		t.Fatalf("stale owner completion accepted=%v err=%v", completed, err)
	}
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-2", "completed", "resumed", now.Add(78*time.Second)); err != nil || !completed {
		t.Fatalf("current owner completion=%v err=%v", completed, err)
	}
	items, err := reopened.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].Status != "completed" || items[0].RunID != secondRunID {
		t.Fatalf("completed queue item=%#v err=%v", items, err)
	}
}

func TestSQLiteAttachRunQueueRunRejectsStaleLeaseAndPreservesCurrentReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attach.db")
	store, err := NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
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

func TestSQLiteStoreRejectsOversizedContextPack(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	err = store.SaveContextPack(context.Background(), domainsuperagent.ContextPack{
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

func TestSQLiteStoreFindAgentRunByIDUsesPrimaryKeyAndRejectsMalformedPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	running := domainsuperagent.AgentRun{RunID: runID, TaskID: taskID, AgentType: "LeadAgent", Status: "running", StartedAt: now}
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

	prefixStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer prefixStore.Close()
	prefixTaskID, prefixRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := prefixStore.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: prefixRunID, TaskID: prefixTaskID, AgentType: "LeadAgent", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, found, err = prefixStore.FindAgentRunByID(ctx, string(runID))
	if err != nil || found || item.RunID != "" {
		t.Fatalf("prefix match item=%#v found=%v err=%v", item, found, err)
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE agent_run SET payload = ? WHERE run_id = ?", "{", string(runID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindAgentRunByID(ctx, string(runID)); err == nil {
		t.Fatal("expected malformed payload error")
	}
}

func TestSQLiteStoreConfiguresSingleConnectionAndBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections=%d want=1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout=%d want=5000", busyTimeout)
	}
}
