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

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	tasksCreatePath          = "/v1/agent/tasks/create"
	tasksCreateLookupPath    = "/v1/agent/tasks/create/lookup"
	tasksCreateMaxBodyBytes  = 16 << 10
	tasksCreateResponseBytes = 16 << 10
)

type taskCreateHandler struct {
	ownerID string
	token   []byte
	tasks   *taskmanager.Manager
}

type taskCreateRequest struct {
	RequestID         modulecore.RequestID    `json:"request_id"`
	Title             string                  `json:"title"`
	Route             domaintask.Route        `json:"route"`
	OwnerID           string                  `json:"owner_id,omitempty"`
	Assignee          string                  `json:"assignee,omitempty"`
	Priority          domaintask.Priority     `json:"priority,omitempty"`
	Plan              string                  `json:"plan,omitempty"`
	ModuleID          string                  `json:"module_id,omitempty"`
	ModuleRoot        string                  `json:"module_root,omitempty"`
	ReadOnly          bool                    `json:"read_only,omitempty"`
	ParentTaskID      modulecore.TaskID       `json:"parent_task_id,omitempty"`
	DependencyTaskIDs []modulecore.TaskID     `json:"dependency_task_ids,omitempty"`
	OriginSessionID   modulecore.SessionID    `json:"origin_session_id,omitempty"`
	OriginThreadID    modulecore.ThreadID     `json:"origin_thread_id,omitempty"`
	OriginTurnID      modulecore.TurnID       `json:"origin_turn_id,omitempty"`
	OriginMessageID   modulecore.MessageID    `json:"origin_message_id,omitempty"`
	WorkstreamID      modulecore.WorkstreamID `json:"workstream_id,omitempty"`
	GoalID            modulecore.GoalID       `json:"goal_id,omitempty"`
	SupersedesTaskID  modulecore.TaskID       `json:"supersedes_task_id,omitempty"`
	GameSpec          *domaintask.GameSpec    `json:"game_spec"`
}

type taskCreateReceipt struct {
	RequestID modulecore.RequestID `json:"request_id"`
	TaskID    modulecore.TaskID    `json:"task_id"`
	Task      domaintask.Task      `json:"task"`
}

func newConfiguredTaskCreateHandler(cfg *config.Config, tasks *taskmanager.Manager) (http.HandlerFunc, error) {
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
	handler := &taskCreateHandler{ownerID: ownerID, token: append([]byte(nil), token...), tasks: tasks}
	return localOnlyHandler(handler.ServeHTTP), nil
}

func (h *taskCreateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAgentOpsError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h == nil || !authorizedAgentOpsBearer(r.Header, h.token) {
		writeAgentOpsError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" || (r.URL.Path != tasksCreatePath && r.URL.Path != tasksCreateLookupPath) {
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
	request, err := decodeTaskCreateRequest(w, r)
	if err != nil {
		if isAgentOpsBodyTooLarge(err) {
			writeAgentOpsError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeAgentOpsError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	draft, shared, err := request.taskInput(h.ownerID)
	if err != nil {
		writeAgentOpsError(w, http.StatusBadRequest, "invalid_game_task")
		return
	}
	if h.tasks == nil {
		writeAgentOpsError(w, http.StatusServiceUnavailable, "task_owner_unavailable")
		return
	}
	if r.URL.Path == tasksCreateLookupPath {
		h.lookup(w, r.Context(), request, draft, shared)
		return
	}
	task, err := h.tasks.EnsureGameTaskCreated(r.Context(), request.RequestID, h.ownerID, draft, shared)
	if err != nil {
		writeTaskCreateDomainError(w, err)
		return
	}
	writeTaskCreateReceipt(w, http.StatusOK, request.RequestID, task)
}

func (h *taskCreateHandler) lookup(w http.ResponseWriter, ctx context.Context, request taskCreateRequest, draft domaintask.Task, shared domaintask.SharedRoleContext) {
	task, found, err := h.tasks.LookupGameTaskCreation(ctx, request.RequestID, h.ownerID, draft, shared)
	if err != nil {
		writeTaskCreateDomainError(w, err)
		return
	}
	if !found {
		writeAgentOpsError(w, http.StatusNotFound, "create_receipt_not_found")
		return
	}
	writeTaskCreateReceipt(w, http.StatusOK, request.RequestID, task)
}

func (request taskCreateRequest) taskInput(authenticatedOwner string) (domaintask.Task, domaintask.SharedRoleContext, error) {
	if err := request.RequestID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.SharedRoleContext{}, fmt.Errorf("request_id is invalid: %w", err)
	}
	if request.OwnerID != "" && request.OwnerID != authenticatedOwner {
		return domaintask.Task{}, domaintask.SharedRoleContext{}, errors.New("owner_id does not match authenticated owner")
	}
	if strings.TrimSpace(request.Title) == "" || strings.ToUpper(strings.TrimSpace(string(request.Route))) != string(domaintask.RouteGAME) || request.GameSpec == nil {
		return domaintask.Task{}, domaintask.SharedRoleContext{}, errors.New("a titled GAME Task and typed game_spec are required")
	}
	draft := domaintask.Task{
		Title: strings.TrimSpace(request.Title), ModuleID: strings.TrimSpace(request.ModuleID), ModuleRoot: strings.TrimSpace(request.ModuleRoot),
		Route: domaintask.RouteGAME, OwnerID: authenticatedOwner, Assignee: strings.TrimSpace(request.Assignee),
		Priority:     domaintask.Priority(strings.ToLower(strings.TrimSpace(string(request.Priority)))),
		ParentTaskID: request.ParentTaskID, DependencyTaskIDs: append([]modulecore.TaskID(nil), request.DependencyTaskIDs...),
		OriginSessionID: request.OriginSessionID, OriginThreadID: request.OriginThreadID, OriginTurnID: request.OriginTurnID,
		OriginMessageID: request.OriginMessageID, WorkstreamID: request.WorkstreamID, GoalID: request.GoalID,
		SupersedesTaskID: request.SupersedesTaskID, ReadOnly: request.ReadOnly,
	}
	shared := domaintask.SharedRoleContext{
		GameSpec: request.GameSpec, UserIntent: draft.Title, ModuleID: draft.ModuleID, ModuleRoot: draft.ModuleRoot,
		CurrentPlan: strings.TrimSpace(request.Plan),
	}
	return draft, shared, nil
}

func decodeTaskCreateRequest(w http.ResponseWriter, r *http.Request) (taskCreateRequest, error) {
	var request taskCreateRequest
	body := http.MaxBytesReader(w, r.Body, tasksCreateMaxBodyBytes)
	encoded, err := io.ReadAll(body)
	if err != nil {
		return request, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return taskCreateRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return taskCreateRequest{}, errors.New("multiple JSON values")
		}
		return taskCreateRequest{}, err
	}
	return request, nil
}

func writeTaskCreateReceipt(w http.ResponseWriter, status int, requestID modulecore.RequestID, task domaintask.Task) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(taskCreateReceipt{RequestID: requestID, TaskID: task.TaskID, Task: task})
}

func writeTaskCreateDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, taskmanager.ErrTaskExists), errors.Is(err, taskmanager.ErrGameTaskCreateReceiptConflict), errors.Is(err, taskpersistence.ErrTaskOperationConflict):
		writeAgentOpsError(w, http.StatusConflict, "task_create_conflict")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeAgentOpsError(w, http.StatusRequestTimeout, "request_interrupted")
	default:
		writeAgentOpsError(w, http.StatusServiceUnavailable, "task_owner_unavailable")
	}
}
