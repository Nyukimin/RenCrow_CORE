package superagent

import (
	"context"
	"os"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreSavesAndListsSuperAgentRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
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
		SubagentID:           "sub_1",
		ParentRunID:          "run_1",
		AgentType:            "ResearchAgent",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "pending",
		CreatedAt:            now,
	}); err != nil {
		t.Fatalf("SaveSubagentTask() error = %v", err)
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
		QueueID:   "queue_1",
		RunID:     "run_1",
		Goal:      "resume run",
		Action:    "resume",
		Status:    "queued",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveRunQueueItem() error = %v", err)
	}
	runs, err := store.ListAgentRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListAgentRuns() = %#v, %v", runs, err)
	}
	tasks, err := store.ListSubagentTasks(context.Background(), 10)
	if err != nil || len(tasks) != 1 {
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
	for _, status := range []string{"queued", "claimed", "completed"} {
		item := domainsuperagent.RunQueueItem{
			QueueID:   "queue_1",
			RunID:     "run_1",
			Goal:      "resume run",
			Action:    "resume",
			Status:    status,
			CreatedAt: now,
		}
		if status == "claimed" {
			item.ClaimedAt = now.Add(time.Second)
			item.LeaseToken = "owner-1"
			item.LeaseUntil = now.Add(time.Minute)
		}
		if status == "completed" {
			item.ClaimedAt = now.Add(time.Second)
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

func TestJSONLStoreListAgentRunsReturnsLatestStatePerRun(t *testing.T) {
	store := NewJSONLStore(t.TempDir(), 3000)
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 9, 28, 0, 0, time.UTC)
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
	running := domainsuperagent.AgentRun{
		RunID:     runID,
		TaskID:    taskID,
		AgentType: "LeadAgent",
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

	prefixStore := NewJSONLStore(t.TempDir(), 3000)
	prefixTaskID, prefixRunID := modulecore.NewTaskID(), modulecore.NewRunID()
	if err := prefixStore.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: prefixRunID, TaskID: prefixTaskID, AgentType: "LeadAgent", Status: "running", StartedAt: now}); err != nil {
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
