package superagent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
)

func TestSQLiteStoreSavesAndListsSuperAgentRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveAgentRun(context.Background(), domainsuperagent.AgentRun{
		RunID:     "run_1",
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
		RunID:         "run_1",
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
	if err := store.SaveTraceEvent(context.Background(), domainsuperagent.TraceEvent{
		EventID:   "evt_1",
		RunID:     "run_1",
		EventType: "lead_agent_started",
		Status:    "completed",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveTraceEvent() error = %v", err)
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
	if err != nil || len(runs) != 1 || runs[0].RunID != "run_1" {
		t.Fatalf("ListAgentRuns() = %#v, %v", runs, err)
	}
	tasks, err := store.ListSubagentTasks(context.Background(), 10)
	if err != nil || len(tasks) != 1 || tasks[0].SubagentID != "sub_1" {
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
	events, err := store.ListTraceEvents(context.Background(), 10)
	if err != nil || len(events) != 1 || events[0].EventID != "evt_1" {
		t.Fatalf("ListTraceEvents() = %#v, %v", events, err)
	}
	queue, err := store.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(queue) != 1 || queue[0].QueueID != "queue_1" {
		t.Fatalf("ListRunQueueItems() = %#v, %v", queue, err)
	}
}

func TestSQLiteStoreRejectsOversizedContextPack(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	err = store.SaveContextPack(context.Background(), domainsuperagent.ContextPack{
		ContextPackID: "ctx_1",
		RunID:         "run_1",
		Summary:       "summary",
		TokenEstimate: 101,
		CreatedAt:     time.Now(),
	})
	if err == nil {
		t.Fatal("expected oversized context pack to fail")
	}
}
