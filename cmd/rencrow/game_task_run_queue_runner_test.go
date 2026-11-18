package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	superagentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestGameTaskQueueComposesImmediateTerminalReceiptAndQueueCompletion(t *testing.T) {
	ctx := context.Background()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	spec, err := domaintask.NormalizeGameSpec(domaintask.GameSpec{
		GameID: "survival_garden", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 120, Mode: "auto",
	})
	if err != nil {
		t.Fatalf("normalize GAME spec: %v", err)
	}
	task, err := tasks.Create(ctx, domaintask.Task{
		Title: "Four-player Survival Garden", OwnerID: "operator", Assignee: "mio", Route: domaintask.RouteGAME,
	}, domaintask.SharedRoleContext{GameSpec: &spec})
	if err != nil {
		t.Fatalf("create GAME Task: %v", err)
	}
	now := time.Date(2026, time.November, 18, 9, 30, 0, 0, time.UTC)
	terminalResult := mustRawJSON(t, gameTaskLaunchResponse{
		OK: true, GameID: spec.GameID, SessionID: "survival_garden_20261118T093000000Z_1", Status: "completed",
	})
	var launchCount int
	observer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/games/launch":
			launchCount++
			var request gameTaskLaunchRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode launch request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.LaunchKey != string(task.TaskID) || request.GameID != spec.GameID ||
				request.Turns != spec.Turns || request.Mode != spec.Mode || !equalGameTaskStrings(request.Personas, spec.Personas) {
				t.Errorf("launch request = %+v; does not match durable Task intent", request)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(gameTaskLaunchResponse{
				OK: true, GameID: spec.GameID, SessionID: "survival_garden_20261118T093000000Z_1", Status: "launching",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/games/launches/"+string(task.TaskID):
			sessionID := "survival_garden_20261118T093000000Z_1"
			_ = json.NewEncoder(w).Encode(gameTaskReceiptResponse{
				LaunchKey: string(task.TaskID), SessionID: sessionID, Status: string(domaintask.GameExecutionCompleted),
				Request:    spec,
				Result:     terminalResult,
				Summary:    json.RawMessage(`{"session_id":"survival_garden_20261118T093000000Z_1","status":"completed"}`),
				GameResult: json.RawMessage(`{"winner":"aka","run_summary":"four-player session completed"}`),
				CreatedAt:  now, UpdatedAt: now.Add(time.Second),
			})
		default:
			t.Errorf("unexpected GAMES request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer observer.Close()

	groupScheduler := &gameTaskAdmissionScheduler{groups: make(map[string][]string)}
	activeSessions := &gameTaskAdmissionSessions{}
	admission := newGameTaskRuntimeAdmission(activeSessions, tasks)
	admission.setScheduler(func() gameTaskGroupScheduler { return groupScheduler })
	runner, err := newGameTaskRunQueueRunner(tasks, observer.URL, admission)
	if err != nil {
		t.Fatalf("create GAME queue runner: %v", err)
	}
	queueStore := &gameTaskRunQueueStore{JSONLStore: superagentpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "superagent"), 0)}
	queueID := modulecore.NewQueueItemID()
	item := domainsuperagent.RunQueueItem{
		QueueItemID: queueID, TaskID: task.TaskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: domainsuperagent.RunQueueGoalGameTask, Action: "game", Status: "queued", Priority: 10,
		IdempotencyKey: domainsuperagent.RunQueueIntentGameTask, CreatedAt: now,
	}
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		t.Fatalf("validate initial GAME queue item: %v", err)
	}
	if err := queueStore.SaveRunQueueItem(ctx, item); err != nil {
		t.Fatalf("save initial GAME queue item: %v", err)
	}
	processor := newSuperAgentRunQueueProcessorWithGameRunner(nil, newBackgroundJobFailureReporter(nil), true, runner)
	queue := superagentapp.NewRunQueueScheduler(queueStore, processor, tasks, superagentapp.RunQueueSchedulerOptions{
		Now: func() time.Time { return now }, ClaimLimit: 1,
	})
	processed, err := queue.RunOnce(ctx)
	if err != nil || processed != 1 {
		t.Fatalf("RunOnce() = (%d, %v), want one completed GAME Task", processed, err)
	}
	if launchCount != 1 {
		t.Fatalf("launch POST count=%d, want one idempotent launch", launchCount)
	}
	finalTask, err := tasks.Get(ctx, task.TaskID)
	if err != nil || finalTask.Status != domaintask.StatusSucceeded || finalTask.Summary == "" {
		t.Fatalf("canonical Task = (%s, %q, %v), want terminal success with title outcome", finalTask.Status, finalTask.Summary, err)
	}
	state, err := tasks.GetGameTaskState(ctx, task.TaskID)
	if err != nil || state.Progress.Status != domaintask.GameExecutionCompleted || state.Progress.SessionID == "" || state.Progress.GameResultJSON == "" {
		t.Fatalf("GAME Task receipt checkpoint = (%+v, %v), want exact terminal receipt", state.Progress, err)
	}
	runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID, Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded || runs[0].CompletedAt.IsZero() {
		t.Fatalf("canonical Task Runs = (%+v, %v), want one completed Run", runs, err)
	}
	queueItems, err := queueStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(queueItems) != 1 || queueItems[0].Status != "completed" || queueItems[0].CompletedAt.IsZero() {
		t.Fatalf("RunQueue receipts = (%+v, %v), want one completed receipt", queueItems, err)
	}
	if len(groupScheduler.groups) != 0 {
		t.Fatalf("terminal GAME actor reservation remains: %+v", groupScheduler.groups)
	}
}

type gameTaskRunQueueStore struct {
	*superagentpersistence.JSONLStore
	events []modulecore.EventEnvelope
}

func (store *gameTaskRunQueueStore) Append(_ context.Context, event modulecore.EventEnvelope) error {
	store.events = append(store.events, event)
	return nil
}

func mustRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func equalGameTaskStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
