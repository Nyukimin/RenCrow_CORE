package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	tasksEnqueuePath        = "/v1/agent/tasks/enqueue"
	tasksEnqueueMaxBodySize = 4096
)

type taskEnqueueRunQueueStore interface {
	FindRunQueueItemByTaskIntent(context.Context, modulecore.TaskID, string) (domainsuperagent.RunQueueItem, bool, error)
	InsertRunQueueItemIfAbsent(context.Context, domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error)
}

type taskEnqueueHandler struct {
	ownerID               string
	token                 []byte
	tasks                 *taskmanager.Manager
	queue                 taskEnqueueRunQueueStore
	agentSchedulerEnabled bool
}

type taskEnqueueRequest struct {
	TaskID modulecore.TaskID `json:"task_id"`
}

type taskEnqueueReceipt struct {
	Status      string                 `json:"status"`
	TaskID      modulecore.TaskID      `json:"task_id"`
	QueueItemID modulecore.QueueItemID `json:"queue_item_id"`
	QueueStatus string                 `json:"queue_status"`
	Priority    int                    `json:"priority"`
	CreatedAt   time.Time              `json:"created_at"`
}

func newConfiguredTaskEnqueueHandler(cfg *config.Config, tasks *taskmanager.Manager, store any) (http.HandlerFunc, error) {
	if cfg == nil || !cfg.LocalAgentOps.Enabled {
		return nil, nil
	}
	ownerID := strings.TrimSpace(cfg.LocalAgentOps.UserID)
	if ownerID == "" || !agentOpsUserIDPattern.MatchString(ownerID) {
		return nil, errors.New("local_agent_ops.user_id is invalid")
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		return nil, err
	}
	queue, _ := store.(taskEnqueueRunQueueStore)
	handler := &taskEnqueueHandler{
		ownerID: ownerID, token: append([]byte(nil), token...), tasks: tasks,
		queue: queue, agentSchedulerEnabled: cfg.Heartbeat.AgentSchedulerEnabled,
	}
	return localOnlyHandler(handler.ServeHTTP), nil
}

func (h *taskEnqueueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAgentOpsError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h == nil || !authorizedAgentOpsBearer(r.Header, h.token) {
		writeAgentOpsError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" || r.URL.Path != tasksEnqueuePath {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeAgentOpsError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		writeAgentOpsError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	request, err := decodeTaskEnqueueRequest(w, r)
	if err != nil {
		if isAgentOpsBodyTooLarge(err) {
			writeAgentOpsError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	if err := request.TaskID.Validate(); err != nil {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_task_id")
		return
	}
	if h.tasks == nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "task_owner_unavailable")
		return
	}
	if h.queue == nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "run_queue_unavailable")
		return
	}
	task, err := h.tasks.Get(r.Context(), request.TaskID)
	if errors.Is(err, taskmanager.ErrNotFound) {
		writeAgentOpsError(w, http.StatusNotFound, "task_not_found")
		return
	}
	if err != nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "task_owner_unavailable")
		return
	}
	if task.OwnerID != h.ownerID {
		writeAgentOpsError(w, http.StatusNotFound, "task_not_found")
		return
	}

	// Retries resolve before lifecycle eligibility checks so a claimed or
	// completed item still returns the exact original receipt.
	existing, found, err := h.queue.FindRunQueueItemByTaskIntent(r.Context(), request.TaskID, domainsuperagent.RunQueueIntentPrivateFirstChat)
	if err != nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "run_queue_unavailable")
		return
	}
	if found {
		writeTaskEnqueueReceipt(w, existing, false)
		return
	}
	if !h.agentSchedulerEnabled {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "heartbeat_scheduler_disabled")
		return
	}
	if err := validateTaskEnqueueEligibility(task, h.ownerID); err != nil {
		// A competing request may have inserted the durable intent while this
		// request read the Task. Resolve that receipt before returning a state
		// refusal, preserving idempotency across a fast claim/completion.
		if existing, found, lookupErr := h.queue.FindRunQueueItemByTaskIntent(r.Context(), request.TaskID, domainsuperagent.RunQueueIntentPrivateFirstChat); lookupErr == nil && found {
			writeTaskEnqueueReceipt(w, existing, false)
			return
		}
		writeAgentOpsError(w, http.StatusConflict, "task_not_eligible")
		return
	}
	runs, err := h.tasks.ListRuns(r.Context(), domaintask.RunFilter{TaskID: request.TaskID})
	if err != nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "task_owner_unavailable")
		return
	}
	if len(runs) != 0 || task.Status != domaintask.StatusQueued {
		if existing, found, lookupErr := h.queue.FindRunQueueItemByTaskIntent(r.Context(), request.TaskID, domainsuperagent.RunQueueIntentPrivateFirstChat); lookupErr == nil && found {
			writeTaskEnqueueReceipt(w, existing, false)
			return
		}
		writeAgentOpsError(w, http.StatusConflict, "task_not_eligible")
		return
	}
	item := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID,
		RunStartReason: domaintask.RunStartReasonFirst,
		WorkstreamID:   string(task.WorkstreamID), Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		Priority: taskEnqueuePriority(task.Priority), IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat,
		CreatedAt: task.CreatedAt.UTC(),
	}
	stored, inserted, err := h.queue.InsertRunQueueItemIfAbsent(r.Context(), item)
	if err != nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "run_queue_write_failed")
		return
	}
	writeTaskEnqueueReceipt(w, stored, inserted)
}

func validateTaskEnqueueEligibility(task domaintask.Task, ownerID string) error {
	return validatePrivateFirstChatTask(task, ownerID, domaintask.StatusQueued)
}

func validatePrivateFirstChatTask(task domaintask.Task, ownerID string, requiredStatus domaintask.Status) error {
	if err := task.Validate(); err != nil {
		return fmt.Errorf("canonical Task is invalid: %w", err)
	}
	if ownerID == "" || task.OwnerID != ownerID || task.Status != requiredStatus || task.Route != domaintask.RouteCHAT {
		return errors.New("Task is outside the private first-run CHAT scope")
	}
	switch task.Assignee {
	case "mio", "shiro", "midori":
	default:
		return errors.New("Task assignee is outside the canonical CHAT actor set")
	}
	if task.OriginSessionID != "" || task.OriginThreadID != "" || task.OriginTurnID != "" || task.OriginMessageID != "" ||
		task.OriginPublication != nil || task.AcceptedOPSClaim != nil || len(task.NativeResumeClaims) != 0 || task.ParentTaskID != "" {
		return errors.New("Task has external or parent provenance outside the private first-run CHAT scope")
	}
	return nil
}

func taskEnqueuePriority(priority domaintask.Priority) int {
	switch priority {
	case domaintask.PriorityCritical:
		return 4
	case domaintask.PriorityHigh:
		return 3
	case domaintask.PriorityNormal:
		return 2
	case domaintask.PriorityLow:
		return 1
	default:
		return 0
	}
}

func decodeTaskEnqueueRequest(w http.ResponseWriter, r *http.Request) (taskEnqueueRequest, error) {
	var request taskEnqueueRequest
	body := http.MaxBytesReader(w, r.Body, tasksEnqueueMaxBodySize)
	encoded, err := io.ReadAll(body)
	if err != nil {
		return request, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return request, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return request, errors.New("multiple JSON values")
		}
		return request, err
	}
	return request, nil
}

func writeTaskEnqueueReceipt(w http.ResponseWriter, item domainsuperagent.RunQueueItem, inserted bool) {
	w.Header().Set("Cache-Control", "no-store")
	status := "existing"
	if inserted {
		status = "enqueued"
	}
	writeJSONStatus(w, http.StatusOK, taskEnqueueReceipt{
		Status: status, TaskID: item.TaskID, QueueItemID: item.QueueItemID,
		QueueStatus: item.Status, Priority: item.Priority, CreatedAt: item.CreatedAt,
	})
}
