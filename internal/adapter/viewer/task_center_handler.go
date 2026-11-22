package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"github.com/google/uuid"
)

const maxTaskCenterRequestBytes = 64 << 10

// TaskCenterService is the application boundary used by the HTTP adapter.
// Identity and authorization are always taken from the trusted context.
type TaskCenterService interface {
	Submit(context.Context, taskcenter.SubmitInput) (taskcenter.MutationResult, error)
	Claim(context.Context, taskcenter.ClaimInput) (taskcenter.MutationResult, error)
	List(context.Context, taskcenter.ListQuery) (taskcenter.ListResult, error)
	Capabilities(context.Context) (taskcenter.Capabilities, error)
	Get(context.Context, string) (taskcenter.Item, error)
	Receipt(context.Context, string, string) (taskcenter.MutationResult, bool, error)
	AcceptResult(context.Context, taskcenter.AcceptResultInput) (taskcenter.MutationResult, error)
	ReadResult(context.Context, string) (taskcenter.ResultView, error)
}

type TaskCenterProjectPlanService interface {
	SubmitGameProjectPlan(context.Context, domainworkstream.GameProjectSubmission) (domainworkstream.ProjectPlanMutationResult, error)
	GameProjectPlanReceipt(context.Context, string) (domainworkstream.ProjectPlanMutationResult, bool, error)
}

type taskCenterHandler struct {
	service           TaskCenterService
	userID            string
	token             []byte
	unavailableReason string
}

// NewTaskCenterHandler creates the authenticated TaskCenter API handler.
// An unavailable service remains explicit as HTTP 503 instead of hiding the
// feature behind an unregistered route.
func NewTaskCenterHandler(service TaskCenterService, userID string, token []byte, unavailableReason string) http.Handler {
	return &taskCenterHandler{
		service: service, userID: strings.TrimSpace(userID), token: append([]byte(nil), token...),
		unavailableReason: strings.TrimSpace(unavailableReason),
	}
}

func (h *taskCenterHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if !security.IsDirectLoopbackRequest(r) {
		writeTaskCenterError(w, http.StatusNotFound, "not_found")
		return
	}
	if h.userID == "" || len(h.token) == 0 {
		writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_auth_unavailable")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeTaskCenterError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !taskCenterClientProfileAllowed(r) {
		writeTaskCenterError(w, http.StatusForbidden, "forbidden")
		return
	}
	if h.service == nil {
		writeTaskCenterError(w, http.StatusServiceUnavailable, safeTaskCenterUnavailableCode(h.unavailableReason))
		return
	}
	ctx, err := taskCenterUserContext(r.Context(), h.userID)
	if err != nil {
		writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_auth_unavailable")
		return
	}
	r = r.WithContext(ctx)

	path := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/v1/task-center/") {
		writeTaskCenterError(w, http.StatusNotFound, "not_found")
		return
	}
	queryValues, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || len(r.URL.RawQuery) > 4096 {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	endpoint := strings.TrimPrefix(path, "/api/v1/task-center/")
	switch endpoint {
	case "capabilities":
		if !taskCenterMethod(w, r, http.MethodGet) || !taskCenterNoQuery(w, queryValues) {
			return
		}
		value, err := h.service.Capabilities(r.Context())
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, value)
	case "items":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		if queryValues.Get("view") == taskcenter.ViewDaily {
			query, err := parseTaskCenterDailyQuery(queryValues)
			if err != nil {
				writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
				return
			}
			dailyReader, ok := h.service.(taskcenter.DailyRead)
			if !ok {
				writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_unavailable")
				return
			}
			value, err := dailyReader.ListDaily(r.Context(), query)
			if err != nil {
				writeTaskCenterDomainError(w, err)
				return
			}
			writeTaskCenterJSON(w, http.StatusOK, value)
			return
		}
		query, err := parseTaskCenterListQuery(queryValues)
		if err != nil {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		value, err := h.service.List(r.Context(), query)
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, value)
	case "item":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"goal_id": true})
		if !ok || !taskCenterRequiredValue(values, "goal_id", 256) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		item, err := h.service.Get(r.Context(), values.Get("goal_id"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, map[string]any{"item": item})
	case "requests":
		if !taskCenterMethod(w, r, http.MethodPost) {
			return
		}
		if !taskCenterNoQuery(w, queryValues) {
			return
		}
		var input taskcenter.SubmitInput
		if !decodeTaskCenterJSON(w, r, &input) {
			return
		}
		result, err := h.service.Submit(r.Context(), input)
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeTaskCenterJSON(w, status, result)
	case "game-project-plans":
		if !taskCenterMethod(w, r, http.MethodPost) || !taskCenterNoQuery(w, queryValues) {
			return
		}
		service, ok := h.service.(TaskCenterProjectPlanService)
		if !ok {
			writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_unsupported")
			return
		}
		var input domainworkstream.GameProjectSubmission
		if !decodeTaskCenterJSON(w, r, &input) {
			return
		}
		result, err := service.SubmitGameProjectPlan(r.Context(), input)
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Replayed {
			status = http.StatusOK
		}
		writeTaskCenterJSON(w, status, result)
	case "game-project-plan":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"idempotency_key": true})
		if !ok || !taskCenterRequiredValue(values, "idempotency_key", 128) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		service, ok := h.service.(TaskCenterProjectPlanService)
		if !ok {
			writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_unsupported")
			return
		}
		result, found, err := service.GameProjectPlanReceipt(r.Context(), values.Get("idempotency_key"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		if !found {
			writeTaskCenterError(w, http.StatusNotFound, "not_found")
			return
		}
		result.Replayed = true
		writeTaskCenterJSON(w, http.StatusOK, result)
	case "claims":
		if !taskCenterMethod(w, r, http.MethodPost) {
			return
		}
		// The authenticated HTTP principal is a user, never an Agent claimant.
		writeTaskCenterError(w, http.StatusForbidden, "forbidden")
	case "result/accept":
		if !taskCenterMethod(w, r, http.MethodPost) || !taskCenterNoQuery(w, queryValues) {
			return
		}
		var input taskcenter.AcceptResultInput
		if !decodeTaskCenterJSON(w, r, &input) {
			return
		}
		result, err := h.service.AcceptResult(r.Context(), input)
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		status := http.StatusOK
		if !result.Replayed {
			status = http.StatusCreated
		}
		writeTaskCenterJSON(w, status, result)
	case "result/acceptance":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"goal_id": true, "idempotency_key": true})
		if !ok || !taskCenterRequiredValue(values, "goal_id", 256) || !taskCenterRequiredValue(values, "idempotency_key", 256) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, found, err := h.service.Receipt(r.Context(), "execution_result_accept", values.Get("idempotency_key"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		if !found || result.Item.GoalID != values.Get("goal_id") {
			writeTaskCenterError(w, http.StatusNotFound, "not_found")
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, result)
	case "result":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"goal_id": true})
		if !ok || !taskCenterRequiredValue(values, "goal_id", 256) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, err := h.service.ReadResult(r.Context(), values.Get("goal_id"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, result)
	case "request":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"operation": true, "idempotency_key": true})
		if !ok || values.Get("operation") != "submit" || !taskCenterRequiredValue(values, "idempotency_key", 256) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, found, err := h.service.Receipt(r.Context(), "submit", values.Get("idempotency_key"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		if !found {
			writeTaskCenterError(w, http.StatusNotFound, "not_found")
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, result)
	case "claim":
		if !taskCenterMethod(w, r, http.MethodGet) {
			return
		}
		values, ok := taskCenterQueryValues(queryValues, map[string]bool{"goal_id": true, "idempotency_key": true})
		if !ok || !taskCenterRequiredValue(values, "goal_id", 256) || !taskCenterRequiredValue(values, "idempotency_key", 256) {
			writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, found, err := h.service.Receipt(r.Context(), "claim", values.Get("idempotency_key"))
		if err != nil {
			writeTaskCenterDomainError(w, err)
			return
		}
		if !found || result.Item.GoalID != values.Get("goal_id") {
			writeTaskCenterError(w, http.StatusNotFound, "not_found")
			return
		}
		writeTaskCenterJSON(w, http.StatusOK, result)
	default:
		writeTaskCenterError(w, http.StatusNotFound, "not_found")
	}
}

func safeTaskCenterUnavailableCode(reason string) string {
	switch reason {
	case "task_center_auth_unavailable", "task_center_workstream_unavailable", "task_center_sqlite_required",
		"task_center_store_unavailable", "task_center_kind_unavailable", "task_center_policy_unconfigured",
		"task_center_policy_invalid":
		return reason
	default:
		return "task_center_unavailable"
	}
}

func taskCenterClientProfileAllowed(r *http.Request) bool {
	client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
	profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
	return clientOK && profileOK && client == "RenCrow_Viewer" && profile == "task-center"
}

func taskCenterUserContext(ctx context.Context, userID string) (context.Context, error) {
	scope, err := tool.NewToolExecutionScope(uuid.NewString(), tool.ActorKindUser, userID, userID,
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		return nil, err
	}
	return tool.WithToolExecutionScope(ctx, scope), nil
}

func taskCenterMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeTaskCenterError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	return false
}

func taskCenterNoQuery(w http.ResponseWriter, values url.Values) bool {
	if len(values) == 0 {
		return true
	}
	writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
	return false
}

func taskCenterQueryValues(values url.Values, allowed map[string]bool) (url.Values, bool) {
	if len(values.Encode()) > 4096 {
		return nil, false
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return nil, false
		}
	}
	return values, true
}

func taskCenterRequiredValue(values url.Values, key string, max int) bool {
	value := values.Get(key)
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value
}

func parseTaskCenterListQuery(values url.Values) (taskcenter.ListQuery, error) {
	values, ok := taskCenterQueryValues(values, map[string]bool{"view": true, "assignee": true, "limit": true, "cursor": true})
	if !ok {
		return taskcenter.ListQuery{}, errors.New("invalid query")
	}
	query := taskcenter.ListQuery{View: values.Get("view"), Limit: 50, Cursor: values.Get("cursor")}
	if query.View != "" && query.View != taskcenter.ViewRemaining && query.View != taskcenter.ViewClaimable && query.View != taskcenter.ViewDaily && query.View != taskcenter.ViewHistory {
		return taskcenter.ListQuery{}, errors.New("invalid view")
	}
	if len(query.Cursor) > 2048 || strings.TrimSpace(query.Cursor) != query.Cursor {
		return taskcenter.ListQuery{}, errors.New("invalid cursor")
	}
	if value, present := values["assignee"]; present {
		assignee := value[0]
		if len(assignee) > 128 || strings.TrimSpace(assignee) != assignee || assignee == "all" || assignee == "self" {
			return taskcenter.ListQuery{}, errors.New("invalid assignee")
		}
		if assignee != "unassigned" && !canonicalTaskCenterActorID(assignee) {
			return taskcenter.ListQuery{}, errors.New("invalid assignee")
		}
		query.Assignee = assignee
	}
	if value, present := values["limit"]; present {
		if len(value[0]) > 3 {
			return taskcenter.ListQuery{}, errors.New("invalid limit")
		}
		limit, err := strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > 200 {
			return taskcenter.ListQuery{}, errors.New("invalid limit")
		}
		query.Limit = limit
	}
	return query, nil
}

func parseTaskCenterDailyQuery(values url.Values) (taskcenter.DailyQuery, error) {
	values, ok := taskCenterQueryValues(values, map[string]bool{"view": true, "kind_id": true, "cursor": true, "limit": true})
	if !ok || values.Get("view") != taskcenter.ViewDaily {
		return taskcenter.DailyQuery{}, errors.New("invalid daily query")
	}
	query := taskcenter.DailyQuery{Limit: 50, Cursor: values.Get("cursor")}
	if len(query.Cursor) > 2048 || strings.TrimSpace(query.Cursor) != query.Cursor {
		return taskcenter.DailyQuery{}, errors.New("invalid cursor")
	}
	if value, present := values["kind_id"]; present {
		query.KindID = value[0]
		if query.KindID == "" || len(query.KindID) > 128 || strings.TrimSpace(query.KindID) != query.KindID {
			return taskcenter.DailyQuery{}, errors.New("invalid kind id")
		}
	}
	if value, present := values["limit"]; present {
		if len(value[0]) > 3 {
			return taskcenter.DailyQuery{}, errors.New("invalid limit")
		}
		limit, err := strconv.Atoi(value[0])
		if err != nil || limit < 1 || limit > 100 {
			return taskcenter.DailyQuery{}, errors.New("invalid limit")
		}
		query.Limit = limit
	}
	return query, nil
}

func canonicalTaskCenterActorID(value string) bool {
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func decodeTaskCenterJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTaskCenterRequestBytes))
	trimmedBody := bytes.TrimSpace(body)
	if err != nil || len(trimmedBody) == 0 || trimmedBody[0] != '{' {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func writeTaskCenterDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, taskcenter.ErrInvalid):
		writeTaskCenterError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, taskcenter.ErrForbidden):
		writeTaskCenterError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, taskcenter.ErrNotFound):
		writeTaskCenterError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, taskcenter.ErrConflict):
		writeTaskCenterError(w, http.StatusConflict, "conflict")
	case errors.Is(err, taskcenter.ErrUnavailable), errors.Is(err, taskcenter.ErrUnsupported):
		writeTaskCenterError(w, http.StatusServiceUnavailable, "task_center_unavailable")
	default:
		writeTaskCenterError(w, http.StatusInternalServerError, "internal_error")
	}
}

func writeTaskCenterError(w http.ResponseWriter, status int, code string) {
	writeTaskCenterJSON(w, status, map[string]string{"error": code})
}

func writeTaskCenterJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		// Headers are committed; there is no safe recovery response to emit.
		return
	}
}
