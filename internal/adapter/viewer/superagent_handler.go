package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type SuperAgentStateLister interface {
	ListAgentRuns(ctx context.Context, limit int) ([]domainsuperagent.AgentRun, error)
	ListSubagentTasks(ctx context.Context, limit int) ([]domainsuperagent.SubagentTask, error)
	ListContextPacks(ctx context.Context, limit int) ([]domainsuperagent.ContextPack, error)
	ListMessageChannels(ctx context.Context, limit int) ([]domainsuperagent.MessageChannel, error)
	ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error)
}

type SuperAgentStateStore interface {
	SuperAgentStateLister
	SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error
	SaveSubagentTask(ctx context.Context, item domainsuperagent.SubagentTask) error
	SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error
	SaveMessageChannel(ctx context.Context, item domainsuperagent.MessageChannel) error
	SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error
}

type SuperAgentLister interface {
	SuperAgentStateLister
	modulecore.EventReader
}

type SuperAgentStore interface {
	SuperAgentStateStore
	modulecore.EventStore
}

type SuperAgentRunController interface {
	PauseRun(runID string, reason string) appsuperagent.RuntimeControlResult
	ResumeRun(runID string, reason string) appsuperagent.RuntimeControlResult
}

// SuperAgentTaskOwner is the only owner allowed to transition the canonical
// Task behind a SuperAgent run. AgentRun is a projection and must not be used
// as a second lifecycle owner by the Viewer adapter.
type SuperAgentTaskOwner interface {
	Wait(context.Context, modulecore.TaskID, string) (domaintask.Task, error)
	Resume(context.Context, modulecore.TaskID) (domaintask.Task, error)
}

type SuperAgentRuntimeConfig struct {
	RunQueueSchedulerEnabled     bool `json:"run_queue_scheduler_enabled"`
	RunQueueSchedulerIntervalSec int  `json:"run_queue_scheduler_interval_sec"`
	RunQueueSchedulerClaimLimit  int  `json:"run_queue_scheduler_claim_limit"`
}

func HandleSuperAgentStatus(store SuperAgentLister) http.HandlerFunc {
	return HandleSuperAgentStatusWithRuntimeConfig(store, SuperAgentRuntimeConfig{})
}

func HandleSuperAgentStatusWithRuntimeConfig(store SuperAgentLister, runtimeConfig SuperAgentRuntimeConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		runs, err := store.ListAgentRuns(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load agent runs", http.StatusInternalServerError)
			return
		}
		tasks, err := store.ListSubagentTasks(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load subagent tasks", http.StatusInternalServerError)
			return
		}
		contexts, err := store.ListContextPacks(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load context packs", http.StatusInternalServerError)
			return
		}
		channels, err := store.ListMessageChannels(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load message channels", http.StatusInternalServerError)
			return
		}
		events, err := store.ListByComponent(r.Context(), "superagent", limit)
		if err != nil {
			http.Error(w, "failed to load trace events", http.StatusInternalServerError)
			return
		}
		queue, err := store.ListRunQueueItems(r.Context(), limit)
		if err != nil {
			http.Error(w, "failed to load run queue", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_runs":       nonNilAgentRuns(runs),
			"subagent_tasks":   nonNilSubagentTasks(tasks),
			"context_packs":    nonNilContextPacks(contexts),
			"message_channels": nonNilMessageChannels(channels),
			"events":           nonNilEventEnvelopes(events),
			"run_queue":        nonNilRunQueueItems(queue),
			"runtime_config":   runtimeConfig,
		})
	}
}

func HandleSuperAgentMessageChannelCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "message channel", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.MessageChannel
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveMessageChannel(ctx, item)
	})
}

type superAgentRunStateRequest struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

func HandleSuperAgentRunPause(store SuperAgentStore) http.HandlerFunc {
	return HandleSuperAgentRunPauseWithTaskOwnerAndController(store, nil, nil)
}

func HandleSuperAgentRunResume(store SuperAgentStore) http.HandlerFunc {
	return HandleSuperAgentRunResumeWithTaskOwnerAndController(store, nil, nil)
}

func HandleSuperAgentRunPauseWithController(store SuperAgentStore, controller SuperAgentRunController) http.HandlerFunc {
	return HandleSuperAgentRunPauseWithTaskOwnerAndController(store, nil, controller)
}

func HandleSuperAgentRunResumeWithController(store SuperAgentStore, controller SuperAgentRunController) http.HandlerFunc {
	return HandleSuperAgentRunResumeWithTaskOwnerAndController(store, nil, controller)
}

func HandleSuperAgentRunPauseWithTaskOwner(store SuperAgentStore, taskOwner SuperAgentTaskOwner) http.HandlerFunc {
	return HandleSuperAgentRunPauseWithTaskOwnerAndController(store, taskOwner, nil)
}

func HandleSuperAgentRunResumeWithTaskOwner(store SuperAgentStore, taskOwner SuperAgentTaskOwner) http.HandlerFunc {
	return HandleSuperAgentRunResumeWithTaskOwnerAndController(store, taskOwner, nil)
}

func HandleSuperAgentRunPauseWithTaskOwnerAndController(store SuperAgentStore, taskOwner SuperAgentTaskOwner, controller SuperAgentRunController) http.HandlerFunc {
	return handleSuperAgentRunPause(store, taskOwner, controller)
}

func HandleSuperAgentRunResumeWithTaskOwnerAndController(store SuperAgentStore, taskOwner SuperAgentTaskOwner, controller SuperAgentRunController) http.HandlerFunc {
	return handleSuperAgentRunResume(store, taskOwner, controller)
}

const defaultSuperAgentPauseReason = "viewer_pause_requested"

func handleSuperAgentRunPause(store SuperAgentStore, taskOwner SuperAgentTaskOwner, controller SuperAgentRunController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req superAgentRunStateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid run state payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		runID := strings.TrimSpace(req.RunID)
		if runID == "" {
			http.Error(w, "run_id is required", http.StatusBadRequest)
			return
		}
		runs, err := store.ListAgentRuns(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load agent runs", http.StatusInternalServerError)
			return
		}
		run, ok := findAgentRunByID(runs, runID)
		if !ok {
			http.Error(w, "agent run not found", http.StatusNotFound)
			return
		}
		if taskOwner == nil {
			http.Error(w, "canonical task owner unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := domainsuperagent.ValidateAgentRun(run); err != nil {
			http.Error(w, "agent run identity is invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			reason = defaultSuperAgentPauseReason
		}
		task, err := taskOwner.Wait(r.Context(), run.TaskID, reason)
		if err != nil {
			http.Error(w, "failed to wait canonical task: "+err.Error(), http.StatusConflict)
			return
		}
		if err := validateOwnedTask(task, run.TaskID, domaintask.StatusWaiting); err != nil {
			http.Error(w, "canonical task owner returned invalid waiting task: "+err.Error(), http.StatusConflict)
			return
		}
		var control appsuperagent.RuntimeControlResult
		if controller != nil {
			control = controller.PauseRun(runID, reason)
		} else {
			control = appsuperagent.RuntimeControlResult{RunID: runID, Action: "none", RequestedAt: time.Now().UTC()}
		}
		now := time.Now().UTC()
		run.Status = "paused"
		run.Summary = reason
		if run.Summary == "" {
			run.Summary = "lead_agent_paused"
		}
		run.CompletedAt = now
		if err := store.SaveAgentRun(r.Context(), run); err != nil {
			http.Error(w, "failed to save agent run state: "+err.Error(), http.StatusBadRequest)
			return
		}
		trace := modulecore.NewRootEventEnvelope("superagent", "run.lead_agent_paused", now, map[string]any{
			"status": "paused", "summary": strings.TrimSpace(run.Summary + " runtime_control=" + control.Action),
		})
		trace.TaskID = run.TaskID
		trace.RunID = run.RunID
		if err := store.Append(r.Context(), trace); err != nil {
			http.Error(w, "failed to save agent run state trace: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":                 string(run.TaskID),
			"run_id":                  runID,
			"status":                  "paused",
			"event_id":                trace.EventID,
			"runtime_control_applied": control.Applied,
			"runtime_control_action":  control.Action,
		})
	}
}

func handleSuperAgentRunResume(store SuperAgentStore, taskOwner SuperAgentTaskOwner, controller SuperAgentRunController) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req superAgentRunStateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid run state payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		runID := strings.TrimSpace(req.RunID)
		if runID == "" {
			http.Error(w, "run_id is required", http.StatusBadRequest)
			return
		}
		runIdentity := modulecore.RunID(runID)
		if err := runIdentity.Validate(); err != nil {
			http.Error(w, "run_id is invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		runs, err := store.ListAgentRuns(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load agent runs", http.StatusInternalServerError)
			return
		}
		run, ok := findAgentRunByID(runs, runID)
		if !ok {
			http.Error(w, "agent run not found", http.StatusNotFound)
			return
		}
		if taskOwner == nil {
			http.Error(w, "canonical task owner unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := domainsuperagent.ValidateAgentRun(run); err != nil {
			http.Error(w, "agent run identity is invalid: "+err.Error(), http.StatusBadRequest)
			return
		}
		if run.Status != "paused" {
			http.Error(w, "only a paused agent run can be resumed", http.StatusConflict)
			return
		}
		if run.ResumePolicy != "checkpoint" || run.CheckpointRevision <= 0 || strings.TrimSpace(run.CheckpointSummary) == "" || strings.TrimSpace(run.NextAction) == "" || run.LastCheckpointAt.IsZero() {
			http.Error(w, "agent run has no durable resumable checkpoint", http.StatusConflict)
			return
		}
		queueID := fmt.Sprintf("resume:%s:%s:%d", run.TaskID, run.RunID, run.CheckpointRevision)
		queue, err := store.ListRunQueueItems(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load resume queue", http.StatusInternalServerError)
			return
		}
		var item domainsuperagent.RunQueueItem
		expectedItem := domainsuperagent.RunQueueItem{
			QueueID:            queueID,
			TaskID:             run.TaskID,
			WorkstreamID:       run.WorkstreamID,
			RunStartReason:     domaintask.RunStartReasonCheckpointResume,
			Goal:               run.Goal,
			Action:             "resume",
			Status:             "queued",
			CheckpointRevision: run.CheckpointRevision,
			CheckpointSummary:  run.CheckpointSummary,
			NextAction:         run.NextAction,
			IdempotencyKey:     queueID,
		}
		existing, exists := findRunQueueItemByID(queue, queueID)
		if exists {
			if !sameResumeQueueIntent(existing, expectedItem) {
				http.Error(w, "resume queue item is not an unclaimed queue intent", http.StatusConflict)
				return
			}
			item = existing
		}
		task, err := taskOwner.Resume(r.Context(), run.TaskID)
		if err != nil {
			http.Error(w, "failed to resume canonical task: "+err.Error(), http.StatusConflict)
			return
		}
		if err := validateOwnedTask(task, run.TaskID, domaintask.StatusQueued); err != nil {
			http.Error(w, "canonical task owner returned invalid queued task: "+err.Error(), http.StatusConflict)
			return
		}
		if !exists {
			item = expectedItem
			item.CreatedAt = time.Now().UTC()
			if err := store.SaveRunQueueItem(r.Context(), item); err != nil {
				http.Error(w, "failed to enqueue agent run resume: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		var control appsuperagent.RuntimeControlResult
		if controller != nil {
			control = controller.ResumeRun(runID, strings.TrimSpace(req.Reason))
		} else {
			control = appsuperagent.RuntimeControlResult{RunID: runID, Action: "none", RequestedAt: time.Now().UTC()}
		}
		now := time.Now().UTC()
		trace := modulecore.NewRootEventEnvelope("superagent", "run.resume_queued", now, map[string]any{
			"status": "queued", "summary": strings.TrimSpace("checkpoint resume runtime_control=" + control.Action),
		})
		trace.TaskID = run.TaskID
		if err := store.Append(r.Context(), trace); err != nil {
			http.Error(w, "failed to save agent run state trace: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":                 string(run.TaskID),
			"source_run_id":           string(run.RunID),
			"run_id":                  "",
			"status":                  "queued",
			"queue_id":                item.QueueID,
			"queue_status":            item.Status,
			"queue_item":              item,
			"event_id":                trace.EventID,
			"runtime_control_applied": control.Applied,
			"runtime_control_action":  control.Action,
		})
	}
}

func sameResumeQueueIntent(existing, expected domainsuperagent.RunQueueItem) bool {
	return existing.QueueID == expected.QueueID &&
		existing.TaskID == expected.TaskID &&
		existing.RunID == "" &&
		existing.RunStartReason == expected.RunStartReason &&
		existing.WorkstreamID == expected.WorkstreamID &&
		existing.Goal == expected.Goal &&
		existing.Action == expected.Action &&
		existing.Status == expected.Status &&
		existing.CheckpointRevision == expected.CheckpointRevision &&
		existing.CheckpointSummary == expected.CheckpointSummary &&
		existing.NextAction == expected.NextAction &&
		existing.IdempotencyKey == expected.IdempotencyKey
}

func validateOwnedTask(task domaintask.Task, expectedID modulecore.TaskID, expectedStatus domaintask.Status) error {
	if task.TaskID != expectedID {
		return fmt.Errorf("task_id %s does not match expected %s", task.TaskID, expectedID)
	}
	if err := task.Validate(); err != nil {
		return err
	}
	if task.Status != expectedStatus {
		return fmt.Errorf("status %s does not match expected %s", task.Status, expectedStatus)
	}
	return nil
}

func saveSuperAgentItem(store SuperAgentStore, name string, save func(context.Context, SuperAgentStore, *json.Decoder) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := save(r.Context(), store, json.NewDecoder(r.Body)); err != nil {
			http.Error(w, "invalid "+name+" payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}
}

func findAgentRunByID(items []domainsuperagent.AgentRun, runID string) (domainsuperagent.AgentRun, bool) {
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if string(item.RunID) == runID {
			return item, true
		}
	}
	return domainsuperagent.AgentRun{}, false
}

func findRunQueueItemByID(items []domainsuperagent.RunQueueItem, queueID string) (domainsuperagent.RunQueueItem, bool) {
	for _, item := range items {
		if item.QueueID == queueID {
			return item, true
		}
	}
	return domainsuperagent.RunQueueItem{}, false
}

func nonNilAgentRuns(items []domainsuperagent.AgentRun) []domainsuperagent.AgentRun {
	if items == nil {
		return []domainsuperagent.AgentRun{}
	}
	return items
}

func nonNilSubagentTasks(items []domainsuperagent.SubagentTask) []domainsuperagent.SubagentTask {
	if items == nil {
		return []domainsuperagent.SubagentTask{}
	}
	return items
}

func nonNilContextPacks(items []domainsuperagent.ContextPack) []domainsuperagent.ContextPack {
	if items == nil {
		return []domainsuperagent.ContextPack{}
	}
	return items
}

func nonNilMessageChannels(items []domainsuperagent.MessageChannel) []domainsuperagent.MessageChannel {
	if items == nil {
		return []domainsuperagent.MessageChannel{}
	}
	return items
}

func nonNilRunQueueItems(items []domainsuperagent.RunQueueItem) []domainsuperagent.RunQueueItem {
	if items == nil {
		return []domainsuperagent.RunQueueItem{}
	}
	return items
}
