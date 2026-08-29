package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	storesuperagent "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSuperAgentResumeE2EHTTPToRestartedScheduler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "superagent-resume-e2e.db")
	eventPath := filepath.Join(t.TempDir(), "event-store.db")
	checkpointAt := time.Date(2026, 8, 23, 15, 30, 0, 0, time.UTC)
	first, err := storesuperagent.NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
	runID := modulecore.NewRunID()
	run := domainsuperagent.AgentRun{
		RunID: string(runID), WorkstreamID: "thread-e2e", AgentType: "LeadAgent", Goal: "finish step two",
		Status: "paused", StartedAt: checkpointAt.Add(-time.Hour), CompletedAt: checkpointAt,
		ResumePolicy: "checkpoint", CheckpointRevision: 2, CheckpointSummary: "step one receipt committed",
		NextAction: "execute step two", LastCheckpointAt: checkpointAt,
	}
	if err := first.SaveAgentRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	firstEvents, err := eventpersistence.NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/viewer/superagent/runs/resume", viewer.HandleSuperAgentRunResume(composeRuntimeSuperAgentStore(first, firstEvents)))
	server := httptest.NewServer(mux)
	defer server.Close()
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := http.Post(server.URL+"/viewer/superagent/runs/resume", "application/json", bytes.NewBufferString(`{"run_id":"`+string(runID)+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("resume status=%d", resp.StatusCode)
		}
	}
	items, err := first.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("idempotent queue=%#v err=%v", items, err)
	}
	crashAt := checkpointAt.Add(time.Minute)
	claimed, err := first.ClaimNextRunQueueItem(context.Background(), crashAt, crashAt.Add(time.Minute), "dead-process")
	if err != nil || claimed == nil {
		t.Fatalf("pre-crash claim=%#v err=%v", claimed, err)
	}
	server.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstEvents.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := storesuperagent.NewSQLiteStore(path, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedEvents, err := eventpersistence.NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedEvents.Close()
	var resumed domainsuperagent.RunQueueItem
	now := crashAt.Add(time.Minute + time.Second)
	scheduler := appsuperagent.NewRunQueueScheduler(composeRuntimeSuperAgentStore(restarted, restartedEvents), appsuperagent.RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem) (string, error) {
		resumed = item
		return "step two committed", nil
	}), appsuperagent.RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1, LeaseDuration: time.Minute, LeaseToken: func() (string, error) { return "restarted-process", nil }})
	count, err := scheduler.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("restart resume count=%d err=%v", count, err)
	}
	if resumed.RunID != run.RunID || resumed.WorkstreamID != run.WorkstreamID || resumed.CheckpointRevision != run.CheckpointRevision || resumed.AttemptCount != 2 {
		t.Fatalf("resumed identity/checkpoint=%#v", resumed)
	}
	final, err := restarted.ListRunQueueItems(context.Background(), 10)
	if err != nil || len(final) != 1 || final[0].Status != "completed" || final[0].Reason != "step two committed" {
		t.Fatalf("final queue=%#v err=%v", final, err)
	}
	finalRuns, err := restarted.ListAgentRuns(context.Background(), 10)
	if err != nil || len(finalRuns) != 1 || finalRuns[0].Status != "completed" || finalRuns[0].Summary != "step two committed" {
		t.Fatalf("final run=%#v err=%v", finalRuns, err)
	}
}
