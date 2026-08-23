package superagent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
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

func TestSQLiteRunQueueClaimSurvivesReopenAndRecoversAfterLeaseExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	first, err := NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
	item := domainsuperagent.RunQueueItem{QueueID: "resume:run-1:7", RunID: "run-1", Goal: "continue", Action: "resume", Status: "queued", CheckpointRevision: 7, CreatedAt: now}
	if err := first.SaveRunQueueItem(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	claimed, err := first.ClaimNextRunQueueItem(context.Background(), now, now.Add(time.Minute), "owner-1")
	if err != nil || claimed == nil || claimed.AttemptCount != 1 {
		t.Fatalf("first claim=%#v err=%v", claimed, err)
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
	recovered, err := reopened.ClaimNextRunQueueItem(context.Background(), now.Add(61*time.Second), now.Add(2*time.Minute), "owner-2")
	if err != nil || recovered == nil || recovered.LeaseToken != "owner-2" || recovered.AttemptCount != 2 || recovered.CheckpointRevision != 7 {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	if completed, err := reopened.CompleteRunQueueItem(context.Background(), recovered.QueueID, "owner-1", "completed", "stale result", now.Add(62*time.Second)); err != nil || completed {
		t.Fatalf("stale owner completion accepted=%v err=%v", completed, err)
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

func TestSQLiteStoreFindAgentRunByIDUsesPrimaryKeyAndRejectsMalformedPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	running := domainsuperagent.AgentRun{RunID: "run_1", AgentType: "LeadAgent", Status: "running", StartedAt: now}
	completed := running
	completed.Status = "completed"
	completed.CompletedAt = now.Add(time.Minute)
	if err := store.SaveAgentRun(ctx, running); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAgentRun(ctx, completed); err != nil {
		t.Fatal(err)
	}
	item, found, err := store.FindAgentRunByID(ctx, "run_1")
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
	if err := prefixStore.SaveAgentRun(ctx, domainsuperagent.AgentRun{RunID: "run_10", AgentType: "LeadAgent", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, found, err = prefixStore.FindAgentRunByID(ctx, "run_1")
	if err != nil || found || item.RunID != "" {
		t.Fatalf("prefix match item=%#v found=%v err=%v", item, found, err)
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE agent_run SET payload = ? WHERE run_id = ?", "{", "run_1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindAgentRunByID(ctx, "run_1"); err == nil {
		t.Fatal("expected malformed payload error")
	}
}

func TestSQLiteStoreFindTraceEventByIDUsesPrimaryKeyAndRejectsMalformedPayload(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	started := domainsuperagent.TraceEvent{EventID: "evt_1", EventType: "started", Status: "running", CreatedAt: now}
	completed := started
	completed.Status = "completed"
	if err := store.SaveTraceEvent(ctx, started); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTraceEvent(ctx, completed); err != nil {
		t.Fatal(err)
	}
	item, found, err := store.FindTraceEventByID(ctx, "evt_1")
	if err != nil || !found || item.Status != "completed" {
		t.Fatalf("item=%#v found=%v err=%v", item, found, err)
	}
	missing, found, err := store.FindTraceEventByID(ctx, "missing")
	if err != nil || found || missing.EventID != "" {
		t.Fatalf("missing=%#v found=%v err=%v", missing, found, err)
	}

	prefixStore, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer prefixStore.Close()
	if err := prefixStore.SaveTraceEvent(ctx, domainsuperagent.TraceEvent{EventID: "evt_10", EventType: "started", Status: "running", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	item, found, err = prefixStore.FindTraceEventByID(ctx, "evt_1")
	if err != nil || found || item.EventID != "" {
		t.Fatalf("prefix match item=%#v found=%v err=%v", item, found, err)
	}

	if _, err := store.db.ExecContext(ctx, "UPDATE trace_event SET payload = ? WHERE event_id = ?", "{", "evt_1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindTraceEventByID(ctx, "evt_1"); err == nil {
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
