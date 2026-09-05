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
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	storesuperagent "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSuperAgentResumeE2EHTTPToRestartedScheduler(t *testing.T) {
	ctx := context.Background()
	superAgentPath := filepath.Join(t.TempDir(), "superagent-resume-e2e.db")
	eventPath := filepath.Join(t.TempDir(), "event-store.db")
	taskPath := filepath.Join(t.TempDir(), "task-state")
	checkpointAt := time.Date(2026, 8, 23, 15, 30, 0, 0, time.UTC)

	first, err := storesuperagent.NewSQLiteStore(superAgentPath, 3000)
	if err != nil {
		t.Fatal(err)
	}
	firstEvents, err := eventpersistence.NewSQLiteStore(eventPath)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}

	firstTaskStore, err := taskpersistence.NewJSONLStore(taskPath)
	if err != nil {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatal(err)
	}
	firstTaskOwner := taskmanager.New(firstTaskStore, taskmanager.DefaultParallelLimits())
	task, err := firstTaskOwner.Create(ctx, domaintask.Task{
		TaskID:   modulecore.NewTaskID(),
		Title:    "finish step two",
		Route:    domaintask.RouteCode,
		Assignee: "mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := firstTaskOwner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SaveAgentRun(ctx, domainsuperagent.AgentRun{
		RunID:              firstRun.RunID,
		TaskID:             task.TaskID,
		WorkstreamID:       "thread-e2e",
		ActorID:            "mio",
		Goal:               task.Title,
		Status:             "running",
		StartedAt:          firstRun.StartedAt,
		ResumePolicy:       "checkpoint",
		CheckpointRevision: 2,
		CheckpointSummary:  "step one receipt committed",
		NextAction:         "execute step two",
		LastCheckpointAt:   checkpointAt,
	}); err != nil {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatal(err)
	}

	runtimeStore := composeRuntimeSuperAgentStore(first, firstEvents)
	mux := http.NewServeMux()
	controller := appsuperagent.NewRunController()
	mux.HandleFunc("/viewer/superagent/runs/pause", viewer.HandleSuperAgentRunPauseWithTaskOwnerAndController(runtimeStore, firstTaskOwner, controller))
	mux.HandleFunc("/viewer/superagent/runs/resume", viewer.HandleSuperAgentRunResumeWithTaskOwnerAndController(runtimeStore, firstTaskOwner, controller))
	server := httptest.NewServer(mux)

	postRunState := func(path, reason string) *http.Response {
		t.Helper()
		body := []byte(`{"run_id":"` + string(firstRun.RunID) + `","reason":"` + reason + `"}`)
		resp, postErr := http.Post(server.URL+path, "application/json", bytes.NewReader(body))
		if postErr != nil {
			t.Fatal(postErr)
		}
		return resp
	}

	pausedResponse := postRunState("/viewer/superagent/runs/pause", "checkpoint pause")
	if pausedResponse.StatusCode != http.StatusOK {
		pausedResponse.Body.Close()
		server.Close()
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("pause status=%d", pausedResponse.StatusCode)
	}
	pausedResponse.Body.Close()
	pausedTask, err := firstTaskOwner.Get(ctx, task.TaskID)
	if err != nil || pausedTask.Status != domaintask.StatusWaiting {
		server.Close()
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("canonical task after pause=%#v err=%v", pausedTask, err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		resumedResponse := postRunState("/viewer/superagent/runs/resume", "checkpoint resume")
		if resumedResponse.StatusCode != http.StatusOK {
			resumedResponse.Body.Close()
			server.Close()
			_ = first.Close()
			_ = firstEvents.Close()
			t.Fatalf("resume status=%d", resumedResponse.StatusCode)
		}
		resumedResponse.Body.Close()
	}
	server.Close()
	queuedTask, err := firstTaskOwner.Get(ctx, task.TaskID)
	if err != nil || queuedTask.Status != domaintask.StatusQueued {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("canonical task after resume=%#v err=%v", queuedTask, err)
	}
	items, err := first.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("idempotent queue=%#v err=%v", items, err)
	}
	if items[0].TaskID != task.TaskID || items[0].RunID != "" || items[0].Status != "queued" {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("queued canonical identity=%#v", items[0])
	}
	queuedEvents, err := firstEvents.ListByComponent(ctx, "superagent", 20)
	if err != nil || len(queuedEvents) < 2 {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("pause/resume events=%#v err=%v", queuedEvents, err)
	}
	var resumeQueuedEvent *modulecore.EventEnvelope
	for index := range queuedEvents {
		event := queuedEvents[index]
		if event.EventType == "run.resume_queued" {
			resumeQueuedEvent = &event
		}
	}
	if resumeQueuedEvent == nil || resumeQueuedEvent.TaskID != task.TaskID || resumeQueuedEvent.RunID != "" {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("resume queued event=%#v", resumeQueuedEvent)
	}

	crashAt := checkpointAt.Add(time.Minute)
	claimed, err := first.ClaimNextRunQueueItem(ctx, crashAt, crashAt.Add(time.Minute), "dead-process")
	if err != nil || claimed == nil {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("pre-crash claim=%#v err=%v", claimed, err)
	}
	if claimed.TaskID != task.TaskID || claimed.RunID != "" || claimed.AttemptCount != 1 {
		_ = first.Close()
		_ = firstEvents.Close()
		t.Fatalf("pre-crash reservation=%#v", claimed)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstEvents.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := storesuperagent.NewSQLiteStore(superAgentPath, 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedEvents, err := eventpersistence.NewSQLiteStore(eventPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedEvents.Close()
	restartedTaskStore, err := taskpersistence.NewJSONLStore(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	restartedTaskOwner := taskmanager.New(restartedTaskStore, taskmanager.DefaultParallelLimits())

	var resumed domainsuperagent.RunQueueItem
	var resumedTrace modulecore.TraceID
	now := crashAt.Add(time.Minute + time.Second)
	restartedRuntimeStore := composeRuntimeSuperAgentStore(restarted, restartedEvents)
	scheduler := appsuperagent.NewRunQueueScheduler(restartedRuntimeStore, appsuperagent.RunQueueProcessorFunc(func(ctx context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		resumed = item
		resumedTrace = traceID
		if _, err := restartedTaskOwner.Succeed(ctx, item.TaskID, "step two committed"); err != nil {
			return "", err
		}
		return "step two committed", nil
	}), restartedTaskOwner, appsuperagent.RunQueueSchedulerOptions{
		Now:           func() time.Time { return now },
		ClaimLimit:    1,
		LeaseDuration: time.Minute,
		LeaseToken:    func() (string, error) { return "restarted-process", nil },
	})
	count, err := scheduler.RunOnce(ctx)
	if err != nil || count != 1 {
		t.Fatalf("restart resume count=%d err=%v", count, err)
	}
	if resumed.TaskID != task.TaskID || resumed.RunID == "" || resumed.RunID == firstRun.RunID || resumed.RunID.Validate() != nil || resumed.AttemptCount != 2 {
		t.Fatalf("resumed canonical identity/checkpoint=%#v first_run=%s", resumed, firstRun.RunID)
	}
	if resumedTrace.Validate() != nil {
		t.Fatalf("resumed trace=%q is not canonical", resumedTrace)
	}

	runs, err := restartedTaskOwner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("canonical run history=%#v err=%v", runs, err)
	}
	var currentRun *domaintask.Run
	for index := range runs {
		candidate := runs[index]
		if candidate.RunID == resumed.RunID {
			currentRun = &candidate
		}
		if candidate.RunID == firstRun.RunID && candidate.Status != domaintask.RunStatusWaiting {
			t.Fatalf("original run was not closed by pause: %#v", candidate)
		}
	}
	if currentRun == nil || currentRun.Status != domaintask.RunStatusSucceeded || currentRun.CompletedAt == nil || currentRun.TaskID != task.TaskID || currentRun.StartReason != domaintask.RunStartReasonLeaseReacquire || currentRun.Summary != "step two committed" {
		t.Fatalf("current canonical run=%#v history=%#v", currentRun, runs)
	}

	events, err := restartedEvents.ListByComponent(ctx, "superagent", 20)
	if err != nil {
		t.Fatal(err)
	}
	queueEvents := make([]modulecore.EventEnvelope, 0, 2)
	for _, event := range events {
		if event.EventType == "run_queue.claimed" || event.EventType == "run_queue.completed" {
			queueEvents = append(queueEvents, event)
		}
	}
	if len(queueEvents) != 2 {
		t.Fatalf("run queue events=%#v want claimed and completed", queueEvents)
	}
	for _, event := range queueEvents {
		if event.TraceID != resumedTrace || event.TaskID != task.TaskID || event.RunID != resumed.RunID {
			t.Fatalf("run queue event identity=%#v want task=%s run=%s trace=%s", event, task.TaskID, resumed.RunID, resumedTrace)
		}
	}
	final, err := restarted.ListRunQueueItems(ctx, 10)
	if err != nil || len(final) != 1 || final[0].Status != "completed" || final[0].TaskID != task.TaskID || final[0].RunID != resumed.RunID || final[0].Reason != "step two committed" {
		t.Fatalf("final queue=%#v err=%v", final, err)
	}
}
