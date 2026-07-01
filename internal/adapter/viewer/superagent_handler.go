package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	appsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/superagent"
)

type SuperAgentLister interface {
	ListAgentRuns(ctx context.Context, limit int) ([]domainsuperagent.AgentRun, error)
	ListSubagentTasks(ctx context.Context, limit int) ([]domainsuperagent.SubagentTask, error)
	ListContextPacks(ctx context.Context, limit int) ([]domainsuperagent.ContextPack, error)
	ListMessageChannels(ctx context.Context, limit int) ([]domainsuperagent.MessageChannel, error)
	ListTraceEvents(ctx context.Context, limit int) ([]domainsuperagent.TraceEvent, error)
	ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error)
}

type SuperAgentStore interface {
	SuperAgentLister
	SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error
	SaveSubagentTask(ctx context.Context, item domainsuperagent.SubagentTask) error
	SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error
	SaveMessageChannel(ctx context.Context, item domainsuperagent.MessageChannel) error
	SaveTraceEvent(ctx context.Context, item domainsuperagent.TraceEvent) error
	SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error
}

type SuperAgentRunController interface {
	PauseRun(runID string, reason string) appsuperagent.RuntimeControlResult
	ResumeRun(runID string, reason string) appsuperagent.RuntimeControlResult
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
		events, err := store.ListTraceEvents(r.Context(), limit)
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
			"trace_events":     nonNilTraceEvents(events),
			"run_queue":        nonNilRunQueueItems(queue),
			"runtime_config":   runtimeConfig,
		})
	}
}

func HandleSuperAgentAgentRunCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "agent run", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.AgentRun
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveAgentRun(ctx, item)
	})
}

func HandleSuperAgentSubagentTaskCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "subagent task", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.SubagentTask
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveSubagentTask(ctx, item)
	})
}

func HandleSuperAgentContextPackCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "context pack", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.ContextPack
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveContextPack(ctx, item)
	})
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

func HandleSuperAgentTraceEventCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "trace event", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.TraceEvent
		if err := dec.Decode(&item); err != nil {
			return err
		}
		return store.SaveTraceEvent(ctx, item)
	})
}

func HandleSuperAgentRunQueueCreate(store SuperAgentStore) http.HandlerFunc {
	return saveSuperAgentItem(store, "run queue item", func(ctx context.Context, store SuperAgentStore, dec *json.Decoder) error {
		var item domainsuperagent.RunQueueItem
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if item.Status == "" {
			item.Status = "queued"
		}
		if item.Action == "" {
			item.Action = "resume"
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = time.Now().UTC()
		}
		return store.SaveRunQueueItem(ctx, item)
	})
}

func HandleSuperAgentRunQueueClaim(store SuperAgentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		items, err := store.ListRunQueueItems(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load run queue", http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		item, ok := nextQueuedRunQueueItem(items, now)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"claimed": false})
			return
		}
		item.Status = "claimed"
		item.ClaimedAt = now
		if err := store.SaveRunQueueItem(r.Context(), item); err != nil {
			http.Error(w, "failed to claim run queue item: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"claimed": true, "item": item})
	}
}

type superAgentRunQueueCompleteRequest struct {
	QueueID string `json:"queue_id"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

func HandleSuperAgentRunQueueComplete(store SuperAgentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "superagent store unavailable", http.StatusServiceUnavailable)
			return
		}
		var req superAgentRunQueueCompleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid run queue complete payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		queueID := strings.TrimSpace(req.QueueID)
		if queueID == "" {
			http.Error(w, "queue_id is required", http.StatusBadRequest)
			return
		}
		items, err := store.ListRunQueueItems(r.Context(), 500)
		if err != nil {
			http.Error(w, "failed to load run queue", http.StatusInternalServerError)
			return
		}
		item, ok := findRunQueueItemByID(items, queueID)
		if !ok {
			http.Error(w, "run queue item not found", http.StatusNotFound)
			return
		}
		status := strings.TrimSpace(req.Status)
		if status == "" {
			status = "completed"
		}
		if status != "completed" && status != "failed" && status != "cancelled" {
			http.Error(w, "status must be completed, failed, or cancelled", http.StatusBadRequest)
			return
		}
		item.Status = status
		item.Reason = strings.TrimSpace(req.Reason)
		item.CompletedAt = time.Now().UTC()
		if err := store.SaveRunQueueItem(r.Context(), item); err != nil {
			http.Error(w, "failed to complete run queue item: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"completed": true, "item": item})
	}
}

type superAgentRunStateRequest struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason,omitempty"`
}

func HandleSuperAgentRunPause(store SuperAgentStore) http.HandlerFunc {
	return HandleSuperAgentRunPauseWithController(store, nil)
}

func HandleSuperAgentRunResume(store SuperAgentStore) http.HandlerFunc {
	return HandleSuperAgentRunResumeWithController(store, nil)
}

func HandleSuperAgentRunPauseWithController(store SuperAgentStore, controller SuperAgentRunController) http.HandlerFunc {
	return handleSuperAgentRunState(store, controller, "paused", "lead_agent_paused")
}

func HandleSuperAgentRunResumeWithController(store SuperAgentStore, controller SuperAgentRunController) http.HandlerFunc {
	return handleSuperAgentRunState(store, controller, "running", "lead_agent_resumed")
}

func handleSuperAgentRunState(store SuperAgentStore, controller SuperAgentRunController, status string, eventType string) http.HandlerFunc {
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
		var control appsuperagent.RuntimeControlResult
		if controller != nil {
			if status == "paused" {
				control = controller.PauseRun(runID, req.Reason)
			} else {
				control = controller.ResumeRun(runID, req.Reason)
			}
		} else {
			control = appsuperagent.RuntimeControlResult{RunID: runID, Action: "none", RequestedAt: time.Now().UTC()}
		}
		now := time.Now().UTC()
		run.Status = status
		run.Summary = strings.TrimSpace(req.Reason)
		if run.Summary == "" {
			run.Summary = eventType
		}
		if status == "paused" {
			run.CompletedAt = now
		} else {
			run.CompletedAt = time.Time{}
		}
		if err := store.SaveAgentRun(r.Context(), run); err != nil {
			http.Error(w, "failed to save agent run state: "+err.Error(), http.StatusBadRequest)
			return
		}
		trace := domainsuperagent.TraceEvent{
			EventID:        "evt_" + eventType + "_" + runID + "_" + formatUnixNano(now),
			RunID:          runID,
			EventType:      eventType,
			Actor:          "ExternalControl",
			PayloadSummary: strings.TrimSpace(run.Summary + " runtime_control=" + control.Action),
			Status:         status,
			CreatedAt:      now,
		}
		if err := store.SaveTraceEvent(r.Context(), trace); err != nil {
			http.Error(w, "failed to save agent run state trace: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"run_id":                  runID,
			"status":                  status,
			"event_id":                trace.EventID,
			"runtime_control_applied": control.Applied,
			"runtime_control_action":  control.Action,
		})
	}
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
	for _, item := range items {
		if item.RunID == runID {
			return item, true
		}
	}
	return domainsuperagent.AgentRun{}, false
}

func nextQueuedRunQueueItem(items []domainsuperagent.RunQueueItem, now time.Time) (domainsuperagent.RunQueueItem, bool) {
	var selected domainsuperagent.RunQueueItem
	found := false
	for _, item := range items {
		if item.Status != "queued" {
			continue
		}
		if !item.NotBefore.IsZero() && item.NotBefore.After(now) {
			continue
		}
		if !found || item.Priority > selected.Priority || (item.Priority == selected.Priority && item.CreatedAt.Before(selected.CreatedAt)) {
			selected = item
			found = true
		}
	}
	return selected, found
}

func findRunQueueItemByID(items []domainsuperagent.RunQueueItem, queueID string) (domainsuperagent.RunQueueItem, bool) {
	for _, item := range items {
		if item.QueueID == queueID {
			return item, true
		}
	}
	return domainsuperagent.RunQueueItem{}, false
}

func formatUnixNano(t time.Time) string {
	return strconv.FormatInt(t.UnixNano(), 10)
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

func nonNilTraceEvents(items []domainsuperagent.TraceEvent) []domainsuperagent.TraceEvent {
	if items == nil {
		return []domainsuperagent.TraceEvent{}
	}
	return items
}

func nonNilRunQueueItems(items []domainsuperagent.RunQueueItem) []domainsuperagent.RunQueueItem {
	if items == nil {
		return []domainsuperagent.RunQueueItem{}
	}
	return items
}
