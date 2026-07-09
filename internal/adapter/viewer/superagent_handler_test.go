package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
)

type stubSuperAgentStore struct {
	runs     []domainsuperagent.AgentRun
	tasks    []domainsuperagent.SubagentTask
	contexts []domainsuperagent.ContextPack
	channels []domainsuperagent.MessageChannel
	events   []domainsuperagent.TraceEvent
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
func (s *stubSuperAgentStore) ListTraceEvents(_ context.Context, _ int) ([]domainsuperagent.TraceEvent, error) {
	return s.events, nil
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
func (s *stubSuperAgentStore) SaveTraceEvent(_ context.Context, item domainsuperagent.TraceEvent) error {
	if err := domainsuperagent.ValidateTraceEvent(item); err != nil {
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
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{RunID: "run_1", AgentType: "LeadAgent", Status: "running"}}}
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

func TestHandleSuperAgentSubagentTaskRequiresScope(t *testing.T) {
	store := &stubSuperAgentStore{}
	payload := []byte(`{"subagent_id":"sub_1","parent_run_id":"run_1","agent_type":"ResearchAgent","task":"調査","termination_condition":"report","status":"pending"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/subagent-tasks", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleSuperAgentSubagentTaskCreate(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSuperAgentTraceEventCreate(t *testing.T) {
	store := &stubSuperAgentStore{}
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	payload := []byte(`{"event_id":"evt_1","run_id":"run_1","event_type":"lead_agent_started","status":"completed","created_at":"` + now + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/trace-events", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	HandleSuperAgentTraceEventCreate(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 {
		t.Fatalf("events=%#v", store.events)
	}
}

func TestHandleSuperAgentRunPauseAndResume(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{
		RunID:     "run_1",
		AgentType: "LeadAgent",
		Status:    "running",
		StartedAt: startedAt,
	}}}

	pauseReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(`{"run_id":"run_1","reason":"user requested pause"}`)))
	pauseRec := httptest.NewRecorder()
	HandleSuperAgentRunPause(store).ServeHTTP(pauseRec, pauseReq)
	if pauseRec.Code != http.StatusOK {
		t.Fatalf("pause status=%d body=%s", pauseRec.Code, pauseRec.Body.String())
	}
	if store.runs[len(store.runs)-1].Status != "paused" {
		t.Fatalf("expected paused run, got %#v", store.runs)
	}
	if len(store.events) != 1 || store.events[0].EventType != "lead_agent_paused" {
		t.Fatalf("expected pause trace, got %#v", store.events)
	}

	resumeReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/resume", bytes.NewReader([]byte(`{"run_id":"run_1","reason":"resume"}`)))
	resumeRec := httptest.NewRecorder()
	HandleSuperAgentRunResume(store).ServeHTTP(resumeRec, resumeReq)
	if resumeRec.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", resumeRec.Code, resumeRec.Body.String())
	}
	if store.runs[len(store.runs)-1].Status != "running" {
		t.Fatalf("expected running run, got %#v", store.runs)
	}
	if len(store.events) != 2 || store.events[1].EventType != "lead_agent_resumed" {
		t.Fatalf("expected resume trace, got %#v", store.events)
	}
}

func TestHandleSuperAgentRunPauseMissingRunFails(t *testing.T) {
	store := &stubSuperAgentStore{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(`{"run_id":"missing"}`)))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunPause(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleSuperAgentRunPauseAppliesRuntimeControl(t *testing.T) {
	startedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubSuperAgentStore{runs: []domainsuperagent.AgentRun{{
		RunID:     "run_1",
		AgentType: "LeadAgent",
		Status:    "running",
		StartedAt: startedAt,
	}}}
	controller := &stubSuperAgentRunController{}

	req := httptest.NewRequest(http.MethodPost, "/viewer/superagent/runs/pause", bytes.NewReader([]byte(`{"run_id":"run_1","reason":"user requested pause"}`)))
	rec := httptest.NewRecorder()
	HandleSuperAgentRunPauseWithController(store, controller).ServeHTTP(rec, req)
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
	if controller.pausedRunID != "run_1" {
		t.Fatalf("controller was not called: %#v", controller)
	}
}

func TestHandleSuperAgentRunQueueClaimAndComplete(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubSuperAgentStore{queue: []domainsuperagent.RunQueueItem{
		{
			QueueID:   "queue_low",
			Goal:      "low priority",
			Action:    "resume",
			Status:    "queued",
			Priority:  1,
			CreatedAt: now,
		},
		{
			QueueID:   "queue_high",
			Goal:      "high priority",
			Action:    "resume",
			Status:    "queued",
			Priority:  9,
			CreatedAt: now.Add(time.Second),
		},
	}}
	claimReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/run-queue/claim", nil)
	claimRec := httptest.NewRecorder()
	HandleSuperAgentRunQueueClaim(store).ServeHTTP(claimRec, claimReq)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("claim status=%d body=%s", claimRec.Code, claimRec.Body.String())
	}
	var claimed map[string]any
	if err := json.Unmarshal(claimRec.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	item := claimed["item"].(map[string]any)
	if item["queue_id"] != "queue_high" || item["status"] != "claimed" {
		t.Fatalf("unexpected claimed item=%#v", item)
	}
	completeReq := httptest.NewRequest(http.MethodPost, "/viewer/superagent/run-queue/complete", bytes.NewReader([]byte(`{"queue_id":"queue_high","status":"completed","reason":"done"}`)))
	completeRec := httptest.NewRecorder()
	HandleSuperAgentRunQueueComplete(store).ServeHTTP(completeRec, completeReq)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("complete status=%d body=%s", completeRec.Code, completeRec.Body.String())
	}
	var completed map[string]any
	if err := json.Unmarshal(completeRec.Body.Bytes(), &completed); err != nil {
		t.Fatalf("decode complete: %v", err)
	}
	completedItem := completed["item"].(map[string]any)
	if completedItem["status"] != "completed" {
		t.Fatalf("unexpected completed item=%#v", completedItem)
	}
}

type stubSuperAgentRunController struct {
	pausedRunID  string
	resumedRunID string
}

func (s *stubSuperAgentRunController) PauseRun(runID string, reason string) appsuperagent.RuntimeControlResult {
	s.pausedRunID = runID
	return appsuperagent.RuntimeControlResult{RunID: runID, Applied: true, Action: "cancel_requested", Reason: reason, RequestedAt: time.Now().UTC()}
}

func (s *stubSuperAgentRunController) ResumeRun(runID string, reason string) appsuperagent.RuntimeControlResult {
	s.resumedRunID = runID
	return appsuperagent.RuntimeControlResult{RunID: runID, Applied: true, Action: "resume_marker_cleared", Reason: reason, RequestedAt: time.Now().UTC()}
}
