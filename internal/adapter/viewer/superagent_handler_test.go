package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubSuperAgentStore struct {
	runs     []domainsuperagent.AgentRun
	tasks    []domainsuperagent.SubagentTask
	contexts []domainsuperagent.ContextPack
	channels []domainsuperagent.MessageChannel
	events   []modulecore.EventEnvelope
	queue    []domainsuperagent.RunQueueItem
}

func (s *stubSuperAgentStore) ListAgentRuns(_ context.Context, _ int) ([]domainsuperagent.AgentRun, error) {
	return s.runs, nil
}
func (s *stubSuperAgentStore) ListSubagentTasks(_ context.Context, _ int) ([]domainsuperagent.SubagentTask, error) {
	return s.tasks, nil
}
func (s *stubSuperAgentStore) ListContextPacks(_ context.Context, _ int) ([]domainsuperagent.ContextPack, error) {
	return s.contexts, nil
}
func (s *stubSuperAgentStore) ListMessageChannels(_ context.Context, _ int) ([]domainsuperagent.MessageChannel, error) {
	return s.channels, nil
}
func (s *stubSuperAgentStore) GetByID(_ context.Context, id modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	for _, item := range s.events {
		if item.EventID == id {
			return item, true, nil
		}
	}
	return modulecore.EventEnvelope{}, false, nil
}
func (s *stubSuperAgentStore) ListByComponent(_ context.Context, component string, _ int) ([]modulecore.EventEnvelope, error) {
	items := make([]modulecore.EventEnvelope, 0, len(s.events))
	for _, item := range s.events {
		if item.ComponentID == component {
			items = append(items, item)
		}
	}
	return items, nil
}
func (s *stubSuperAgentStore) ListRunQueueItems(_ context.Context, _ int) ([]domainsuperagent.RunQueueItem, error) {
	return s.queue, nil
}
func (s *stubSuperAgentStore) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return err
	}
	s.runs = append(s.runs, item)
	return nil
}
func (s *stubSuperAgentStore) SaveSubagentTask(_ context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	s.tasks = append(s.tasks, item)
	return nil
}
func (s *stubSuperAgentStore) SaveContextPack(_ context.Context, item domainsuperagent.ContextPack) error {
	if err := domainsuperagent.ValidateContextPack(item, 3000); err != nil {
		return err
	}
	s.contexts = append(s.contexts, item)
	return nil
}
func (s *stubSuperAgentStore) SaveMessageChannel(_ context.Context, item domainsuperagent.MessageChannel) error {
	if err := domainsuperagent.ValidateMessageChannel(item); err != nil {
		return err
	}
	s.channels = append(s.channels, item)
	return nil
}
func (s *stubSuperAgentStore) Append(_ context.Context, item modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(item); err != nil {
		return err
	}
	s.events = append(s.events, item)
	return nil
}
func (s *stubSuperAgentStore) SaveRunQueueItem(_ context.Context, item domainsuperagent.RunQueueItem) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return err
	}
	s.queue = append(s.queue, item)
	return nil
}

func TestHandleSuperAgentStatus(t *testing.T) {
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), AgentType: "LeadAgent", Status: "running"}}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/superagent", nil)
	rec := httptest.NewRecorder()
	HandleSuperAgentStatus(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body["agent_runs"].([]any)) != 1 {
		t.Fatalf("body=%#v", body)
	}
	if body["run_queue"] == nil {
		t.Fatalf("missing run_queue: %#v", body)
	}
	if body["runtime_config"] == nil {
		t.Fatalf("missing runtime_config: %#v", body)
	}
}

func TestHandleSuperAgentStatusWithRuntimeConfigShowsSchedulerConfig(t *testing.T) {
	store := &stubSuperAgentStore{}
	req := httptest.NewRequest(http.MethodGet, "/viewer/superagent", nil)
	rec := httptest.NewRecorder()
	HandleSuperAgentStatusWithRuntimeConfig(store, SuperAgentRuntimeConfig{
		RunQueueSchedulerEnabled:     true,
		RunQueueSchedulerIntervalSec: 3,
		RunQueueSchedulerClaimLimit:  2,
	}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"runtime_config"`, `"run_queue_scheduler_enabled":true`, `"run_queue_scheduler_interval_sec":3`, `"run_queue_scheduler_claim_limit":2`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s: %s", want, body)
		}
	}
}

func TestHandleSuperAgentRunPauseAndResume(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	owner := newStubSuperAgentTaskOwner(taskID, startedAt)
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{
		RunID:              runID,
		TaskID:             taskID,
		AgentType:          "LeadAgent",
		Goal:               "continue durable work",
		Status:             "running",
		StartedAt:          startedAt,
		ResumePolicy:       "checkpoint",
		CheckpointRevision: 3,
		CheckpointSummary:  "step 2 committed",
		NextAction:         "execute step 3",
		LastCheckpointAt:   startedAt,
	}}}

	pauseReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q,"reason":"user requested pause"}`, runID))))
	pauseRec := httptest.NewRecorder()
	controller := &stubSuperAgentRunController{}
	HandleSuperAgentRunPauseWithTaskOwnerAndController(store, owner, controller).ServeHTTP(pauseRec, pauseReq)
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", pauseRec.Code, pauseRec.Body.String())
	}
	pausedRun := store.runs[len(store.runs)-1]
	if pausedRun.Status != "paused" || pausedRun.TaskID != taskID || pausedRun.CompletedAt.IsZero() {
		t.Fatalf("expected paused run, got %#v", store.runs)
	}
	if len(owner.waitCalls) != 1 || owner.waitCalls[0] != taskID || owner.lastWaitReason != "user requested pause" || owner.tasks[taskID].Status != domaintask.StatusWaiting {
		t.Fatalf("expected canonical task waiting, calls=%#v task=%#v", owner.waitCalls, owner.tasks[taskID])
	}
	if controller.pausedRunID != string(runID) {
		t.Fatalf("controller was not called after task wait: %#v", controller)
	}
	if len(store.events) != 1 || store.events[0].EventType != "run.lead_agent_paused" || store.events[0].TaskID != taskID || store.events[0].RunID != runID {
		t.Fatalf("expected pause trace, got %#v", store.events)
	}
	if _, found := store.events[0].Payload["actor_label"]; found {
		t.Fatalf("pause trace contains actor identity payload: %#v", store.events[0])
	}
	if _, found := store.events[0].Payload["run_reference"]; found {
		t.Fatalf("pause trace contains run identity payload: %#v", store.events[0])
	}
	var pauseResponse map[string]any
	if err := json.Unmarshal(pauseRec.Body.Bytes(), &pauseResponse); err != nil {
		t.Fatal(err)
	}
	if pauseResponse["task_id"] != string(taskID) || pauseResponse["run_id"] != string(runID) {
		t.Fatalf("pause response identities=%#v", pauseResponse)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q,"reason":"resume"}`, runID))))
	resumeRec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwnerAndController(store, owner, controller).ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resumeRec.Code, resumeRec.Body.String())
	}
	if store.runs[len(store.runs)-1].Status != "paused" || !store.runs[len(store.runs)-1].CompletedAt.Equal(pausedRun.CompletedAt) {
		t.Fatalf("resume must not mutate old run projection, got %#v", store.runs)
	}
	if owner.tasks[taskID].Status != domaintask.StatusQueued || len(owner.resumeCalls) != 1 || owner.resumeCalls[0] != taskID {
		t.Fatalf("expected canonical task queued, calls=%#v task=%#v", owner.resumeCalls, owner.tasks[taskID])
	}
	if controller.resumedRunID != string(runID) {
		t.Fatalf("controller marker was not cleared: %#v", controller)
	}
	if len(store.events) != 2 || store.events[1].EventType != "run.resume_queued" || store.events[1].TaskID != taskID || store.events[1].RunID != "" {
		t.Fatalf("expected resume trace, got %#v", store.events)
	}
	if _, found := store.events[1].Payload["actor_label"]; found {
		t.Fatalf("resume trace contains actor identity payload: %#v", store.events[1])
	}
	if _, found := store.events[1].Payload["run_reference"]; found {
		t.Fatalf("resume trace contains run identity payload: %#v", store.events[1])
	}
	for _, field := range []string{"run_id", "source_run_id"} {
		if _, found := store.events[1].Payload[field]; found {
			t.Fatalf("resume trace contains %s identity payload: %#v", field, store.events[1])
		}
	}
	if len(store.queue) != 1 || store.queue[0].QueueID != "resume:"+string(taskID)+":"+string(runID)+":3" || store.queue[0].TaskID != taskID || store.queue[0].RunID != "" || store.queue[0].RunStartReason != domaintask.RunStartReasonCheckpointResume || store.queue[0].Status != "queued" || store.queue[0].CheckpointRevision != 3 {
		t.Fatalf("resume queue=%#v", store.queue)
	}
	var resumeResponse map[string]any
	if err := json.Unmarshal(resumeRec.Body.Bytes(), &resumeResponse); err != nil {
		t.Fatal(err)
	}
	if resumeResponse["task_id"] != string(taskID) || resumeResponse["source_run_id"] != string(runID) || resumeResponse["run_id"] != "" || resumeResponse["status"] != "queued" || resumeResponse["queue_id"] != store.queue[0].QueueID || resumeResponse["queue_status"] != "queued" || resumeResponse["queue_item"] == nil {
		t.Fatalf("resume response receipt=%#v", resumeResponse)
	}
	resumeTraceID := store.events[1].TraceID
	if resumeTraceID == store.events[0].TraceID {
		t.Fatalf("pause and resume must be separate request traces: %#v", store.events)
	}
	secondReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q,"reason":"duplicate transport retry"}`, runID))))
	secondRec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwnerAndController(store, owner, controller).ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK || len(store.queue) != 1 || len(store.runs) != 2 {
		t.Fatalf("idempotent resume status=%d queue=%#v body=%s", secondRec.Code, store.queue, secondRec.Body.String())
	}
	store.queue[0].CheckpointSummary = "tampered checkpoint"
	mismatchReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
	mismatchRec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwnerAndController(store, owner, controller).ServeHTTP(mismatchRec, mismatchReq)
	if mismatchRec.Code != http.StatusConflict || len(store.events) != 3 {
		t.Fatalf("mismatched idempotent intent status=%d events=%#v body=%s", mismatchRec.Code, store.events, mismatchRec.Body.String())
	}
}

func TestHandleSuperAgentRunResumeRequiresPausedProjection(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	for _, status := range []string{"running", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
			owner := newStubSuperAgentTaskOwner(taskID, startedAt)
			run := domainsuperagent.AgentRun{
				RunID: runID, TaskID: taskID, AgentType: "LeadAgent", Goal: "work", Status: status, StartedAt: startedAt,
				ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "checkpoint", NextAction: "continue", LastCheckpointAt: startedAt,
			}
			if status != "running" {
				run.CompletedAt = startedAt.Add(time.Minute)
			}
			store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{run}}
			req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
			rec := httptest.NewRecorder()
			HandleSuperAgentRunResumeWithTaskOwner(store, owner).ServeHTTP(rec, req)
			if rec.Code != http.StatusConflict || len(store.queue) != 0 || len(owner.resumeCalls) != 0 {
				t.Fatalf("status=%d body=%s queue=%#v resume_calls=%#v", rec.Code, rec.Body.String(), store.queue, owner.resumeCalls)
			}
		})
	}
}

func TestHandleSuperAgentRunResumeRejectsMissingCheckpoint(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	owner := newStubSuperAgentTaskOwner(taskID, startedAt)
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{RunID: runID, TaskID: taskID, AgentType: "LeadAgent", Goal: "work", Status: "paused", StartedAt: startedAt, CompletedAt: startedAt}}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwner(store, owner).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || len(store.queue) != 0 {
		t.Fatalf("status=%d queue=%#v body=%s", rec.Code, store.queue, rec.Body.String())
	}
}

func TestHandleSuperAgentRunResumeRejectsMismatchedExistingIntentBeforeTaskResume(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	run := domainsuperagent.AgentRun{
		RunID: runID, TaskID: taskID, AgentType: "LeadAgent", Goal: "work", Status: "paused", StartedAt: startedAt,
		CompletedAt: startedAt.Add(time.Minute), ResumePolicy: "checkpoint", CheckpointRevision: 2,
		CheckpointSummary: "checkpoint", NextAction: "continue", LastCheckpointAt: startedAt,
	}
	owner := newStubSuperAgentTaskOwner(taskID, startedAt)
	task := owner.tasks[taskID]
	task.Status = domaintask.StatusWaiting
	task.WaitingReason = "paused"
	owner.tasks[taskID] = task
	queueID := "resume:" + string(taskID) + ":" + string(runID) + ":2"
	store := &stubSuperAgentStore{
		runs: []domainsuperagent.AgentRun{run},
		queue: []domainsuperagent.RunQueueItem{{
			QueueID: queueID, TaskID: taskID, RunStartReason: domaintask.RunStartReasonCheckpointResume,
			Goal: "work", Action: "resume", Status: "queued", CheckpointRevision: 2,
			CheckpointSummary: "tampered", NextAction: "continue", IdempotencyKey: queueID, CreatedAt: startedAt,
		}},
	}
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwner(store, owner).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if owner.tasks[taskID].Status != domaintask.StatusWaiting || len(owner.resumeCalls) != 0 {
		t.Fatalf("mismatched intent changed canonical task: task=%#v resume_calls=%#v", owner.tasks[taskID], owner.resumeCalls)
	}
}

func TestHandleSuperAgentRunPauseMissingRunFails(t *testing.T) {
	owner := newStubSuperAgentTaskOwner(modulecore.NewTaskID(), time.Now().UTC())
	store := &stubSuperAgentStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(`{"run_id":"missing"}`)))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunPauseWithTaskOwner(store, owner).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSuperAgentRunPauseAppliesRuntimeControl(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	owner := newStubSuperAgentTaskOwner(taskID, startedAt)
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{
		RunID:     runID,
		TaskID:    taskID,
		AgentType: "LeadAgent",
		Status:    "running",
		StartedAt: startedAt,
	}}}
	controller := &stubSuperAgentRunController{}

	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q,"reason":"user requested pause"}`, runID))))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunPauseWithTaskOwnerAndController(store, owner, controller).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["runtime_control_applied"] != true {
		t.Fatalf("expected runtime control applied, got %#v", got)
	}
	if got["runtime_control_action"] != "cancel_requested" {
		t.Fatalf("expected cancel action, got %#v", got)
	}
	if controller.pausedRunID != string(runID) {
		t.Fatalf("controller was not called: %#v", controller)
	}
}

func TestHandleSuperAgentRunStateOwnerFailureLeavesProjectionAndQueueUntouched(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	run := domainsuperagent.AgentRun{
		RunID: runID, TaskID: taskID, AgentType: "LeadAgent", Goal: "durable work", Status: "running", StartedAt: startedAt,
		ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "checkpoint", NextAction: "continue", LastCheckpointAt: startedAt,
	}
	controller := &stubSuperAgentRunController{}
	owner := newStubSuperAgentTaskOwner(taskID, startedAt)
	owner.waitErr = fmt.Errorf("task store unavailable")
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{run}}
	pauseReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
	pauseRec := httptest.NewRecorder()
	HandleSuperAgentRunPauseWithTaskOwnerAndController(store, owner, controller).ServeHTTP(pauseRec, pauseReq)
	if pauseRec.Code == http.StatusOK || len(store.runs) != 1 || len(store.queue) != 0 || controller.pausedRunID != "" {
		t.Fatalf("pause owner failure mutated state: status=%d runs=%#v queue=%#v controller=%#v", pauseRec.Code, store.runs, store.queue, controller)
	}

	owner.waitErr = nil
	owner.resumeErr = fmt.Errorf("task store unavailable")
	run.Status = "paused"
	run.CompletedAt = startedAt.Add(time.Minute)
	store.runs[0] = run
	resumeReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(fmt.Sprintf(`{"run_id":%q}`, runID))))
	resumeRec := httptest.NewRecorder()
	HandleSuperAgentRunResumeWithTaskOwnerAndController(store, owner, controller).ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code == http.StatusOK || len(store.runs) != 1 || len(store.queue) != 0 || controller.resumedRunID != "" {
		t.Fatalf("resume owner failure mutated state: status=%d runs=%#v queue=%#v controller=%#v", resumeRec.Code, store.runs, store.queue, controller)
	}
}

type stubSuperAgentRunController struct {
	pausedRunID  string
	resumedRunID string
}

type stubSuperAgentTaskOwner struct {
	tasks          map[modulecore.TaskID]domaintask.Task
	waitCalls      []modulecore.TaskID
	resumeCalls    []modulecore.TaskID
	lastWaitReason string
	waitErr        error
	resumeErr      error
}

func newStubSuperAgentTaskOwner(taskID modulecore.TaskID, now time.Time) *stubSuperAgentTaskOwner {
	return &stubSuperAgentTaskOwner{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: {
			TaskID:          taskID,
			Title:           "superagent work",
			Route:           domaintask.RouteOperations,
			Status:          domaintask.StatusRunning,
			Priority:        domaintask.PriorityNormal,
			InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}}
}

func (s *stubSuperAgentTaskOwner) Wait(_ context.Context, taskID modulecore.TaskID, reason string) (domaintask.Task, error) {
	s.waitCalls = append(s.waitCalls, taskID)
	s.lastWaitReason = reason
	if s.waitErr != nil {
		return domaintask.Task{}, s.waitErr
	}
	task, ok := s.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	task.Status = domaintask.StatusWaiting
	task.WaitingReason = reason
	task.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = task
	return task, nil
}

func (s *stubSuperAgentTaskOwner) Resume(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	s.resumeCalls = append(s.resumeCalls, taskID)
	if s.resumeErr != nil {
		return domaintask.Task{}, s.resumeErr
	}
	task, ok := s.tasks[taskID]
	if !ok {
		return domaintask.Task{}, domaintask.ErrNotFound
	}
	task.Status = domaintask.StatusQueued
	task.WaitingReason = ""
	task.UpdatedAt = time.Now().UTC()
	s.tasks[taskID] = task
	return task, nil
}

func (s *stubSuperAgentRunController) PauseRun(runID string, reason string) appsuperagent.RuntimeControlResult {
	s.pausedRunID = runID
	return appsuperagent.RuntimeControlResult{RunID: runID, Applied: true, Action: "cancel_requested", Reason: reason, RequestedAt: time.Now().UTC()}
}

func (s *stubSuperAgentRunController) ResumeRun(runID string, reason string) appsuperagent.RuntimeControlResult {
	s.resumedRunID = runID
	return appsuperagent.RuntimeControlResult{RunID: runID, Applied: true, Action: "resume_marker_cleared", Reason: reason, RequestedAt: time.Now().UTC()}
}
