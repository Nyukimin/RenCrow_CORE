package viewer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"github.com/google/uuid"
)

const memoryOwnerRoute = "/v1/memory/user"

type MemoryOwnerStore interface {
	OwnerListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error)
	OwnerFindUserMemory(context.Context, string, string) (domainmemory.UserMemory, error)
	OwnerProposeUserMemory(context.Context, string, string, string, string, string, string) (domainmemory.UserMemoryOwnerResult, error)
	OwnerTransitionUserMemory(context.Context, string, string, string, string, string, string, string) (domainmemory.UserMemoryOwnerResult, error)
	OwnerArchiveUserMemory(context.Context, string, string, string, string, string) (domainmemory.UserMemoryOwnerResult, error)
	OwnerRecallUserMemories(context.Context, string, string, string, int) (domainmemory.UserMemoryOwnerRecallResult, error)
	OwnerListRecallTraces(context.Context, string, int) ([]domainmemory.UserMemoryTraceSummary, error)
	OwnerFindRecallTrace(context.Context, string, string) (domainmemory.UserMemoryTraceDetail, error)
	OwnerPlanUserMemoryLifecycle(context.Context, string, string, string) (domainmemory.UserMemoryLifecyclePlanResponse, error)
	OwnerRunUserMemoryLifecycle(context.Context, string, string, string, string, string, bool) (domainmemory.UserMemoryLifecycleRunResponse, error)
	OwnerExportConversationArchiveParquet(context.Context, string, string, string) (domainmemory.ConversationArchiveParquetExportResult, error)
	OwnerVerifyConversationArchiveParquet(context.Context, string, string, string, string) (domainmemory.ConversationArchiveParquetVerifyResult, error)
	BackfillKnowledgeCommonRaw(context.Context, string, string, string, bool) (domainmemory.KnowledgeCommonRawBackfillResult, error)
}

type memoryOwnerHandler struct {
	store  MemoryOwnerStore
	userID string
	token  []byte
}

// NewMemoryOwnerHandler constructs the authenticated CORE owner API. The
// token is supplied by startup wiring and is never read from disk per request.
func NewMemoryOwnerHandler(store MemoryOwnerStore, userID string, token []byte) http.HandlerFunc {
	return (&memoryOwnerHandler{
		store:  store,
		userID: strings.TrimSpace(userID),
		token:  append([]byte(nil), token...),
	}).ServeHTTP
}

func (h *memoryOwnerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerDirectLocalRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if !h.bearerAuthorized(r) {
		writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !memoryOwnerClientProfileAllowed(r) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if h.store == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "store_unavailable")
		return
	}
	ctx, err := h.ownerContext(r)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}

	escapedPath := r.URL.EscapedPath()
	path := strings.TrimSuffix(escapedPath, "/")
	switch {
	case path == memoryOwnerRoute && r.Method == http.MethodGet:
		h.list(ctx, w, r)
	case path == memoryOwnerRoute+"/recall" && r.Method == http.MethodGet:
		h.recall(ctx, w, r)
	case path == memoryOwnerRoute+"/traces" && r.Method == http.MethodGet:
		h.traceList(ctx, w, r)
	case strings.HasPrefix(path, memoryOwnerRoute+"/traces/") && r.Method == http.MethodGet:
		parts, ok := memoryOwnerPathSegments(r)
		if !ok || len(parts) != 2 || parts[0] != "traces" {
			writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
			return
		}
		h.traceShow(ctx, w, r, parts[1])
	case path == memoryOwnerRoute+"/propose" && r.Method == http.MethodPost:
		h.propose(ctx, w, r)
	case path == "/viewer/memory/user/archive" && r.Method == http.MethodPost:
		h.archive(ctx, w, r)
	case escapedPath == "/viewer/memory/lifecycle/plan" && r.Method == http.MethodPost:
		h.lifecyclePlan(ctx, w, r)
	case escapedPath == "/viewer/memory/lifecycle/run" && r.Method == http.MethodPost:
		h.lifecycleRun(ctx, w, r)
	case escapedPath == "/viewer/memory/knowledge-raw/backfill" && r.Method == http.MethodPost:
		h.knowledgeBackfill(ctx, w, r)
	case escapedPath == memoryOwnerParquetExportRoute+"/parquet" && r.Method == http.MethodPost:
		h.parquetExport(ctx, w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(escapedPath, memoryOwnerParquetExportRoute+"/"):
		targetID, ok := memoryOwnerParquetTargetID(escapedPath)
		if !ok {
			writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
			return
		}
		h.parquetVerify(ctx, w, r, targetID)
	case strings.HasPrefix(path, memoryOwnerRoute+"/"):
		parts, ok := memoryOwnerPathSegments(r)
		if !ok {
			writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
			return
		}
		h.item(ctx, w, r, parts)
	default:
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
	}
}

func (h *memoryOwnerHandler) lifecyclePlan(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := decodeEmptyMemoryOwnerObjectBody(w, r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerPlanUserMemoryLifecycle(ctx, requestID, h.userID, h.userID)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *memoryOwnerHandler) lifecycleRun(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request struct {
		PlanRequestID string `json:"plan_request_id"`
		Reason        string `json:"reason"`
		Apply         *bool  `json:"apply"`
	}
	if err := decodeStrictLifecycleJSON(w, r, &request, 8<<10); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request.PlanRequestID = strings.TrimSpace(request.PlanRequestID)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.PlanRequestID == "" || request.Reason == "" || request.Apply == nil || !*request.Apply {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerRunUserMemoryLifecycle(ctx, requestID, h.userID, h.userID, request.PlanRequestID, request.Reason, true)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// knowledgeBackfill links existing l1_knowledge_item rows into Common Raw.
// The body is a strict JSON object {"apply": bool}; apply defaults to false
// so the dry-run coverage report is the default operation.
func (h *memoryOwnerHandler) knowledgeBackfill(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request struct {
		Apply *bool `json:"apply"`
	}
	if err := decodeStrictKnowledgeBackfillJSON(w, r, &request); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	apply := request.Apply != nil && *request.Apply
	result, err := h.store.BackfillKnowledgeCommonRaw(ctx, requestID, h.userID, h.userID, apply)
	if err != nil {
		writeChatGPTOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeStrictKnowledgeBackfillJSON(w http.ResponseWriter, r *http.Request, target interface{}) error {
	if r.Body == nil {
		return nil
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	if err := rejectDuplicateObjectKeys(payload, map[string]struct{}{"apply": {}}); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeEmptyMemoryOwnerObjectBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	var payload map[string]json.RawMessage
	if err := decoder.Decode(&payload); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if payload == nil || len(payload) != 0 {
		return errors.New("lifecycle plan body must be an empty object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeStrictLifecycleJSON(w http.ResponseWriter, r *http.Request, target interface{}, maxBytes int64) error {
	if r.Body == nil {
		return errors.New("lifecycle run body is required")
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		return err
	}
	if err := rejectDuplicateLifecycleObjectKeys(payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateLifecycleObjectKeys(payload []byte) error {
	return rejectDuplicateObjectKeys(payload, map[string]struct{}{
		"plan_request_id": {}, "reason": {}, "apply": {},
	})
}

func rejectDuplicateObjectKeys(payload []byte, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("lifecycle object key must be a string")
		}
		if _, ok := allowed[key]; !ok {
			return errors.New("unknown lifecycle object key")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate lifecycle object key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func (h *memoryOwnerHandler) archive(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request struct {
		MemoryID string `json:"memory_id"`
		Reason   string `json:"reason"`
	}
	if err := decodeStrictMemoryOwnerJSON(w, r, &request, 8<<10); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request.MemoryID = strings.TrimSpace(request.MemoryID)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.MemoryID == "" || request.Reason == "" {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerArchiveUserMemory(ctx, requestID, h.userID, h.userID, request.MemoryID, request.Reason)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *memoryOwnerHandler) recall(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodGet) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query(), "query", "limit"); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" || len([]rune(query)) > 512 {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limit, err := parseStrictMemoryOwnerLimit(r.URL.Query().Get("limit"), domainmemory.UserMemoryRecallDefaultLimit, domainmemory.UserMemoryRecallMaxLimit)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerRecallUserMemories(ctx, requestID, h.userID, query, limit)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *memoryOwnerHandler) traceList(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodGet) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query(), "limit"); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limit, err := parseStrictMemoryOwnerLimit(r.URL.Query().Get("limit"), domainmemory.UserMemoryTraceDefaultLimit, domainmemory.UserMemoryTraceMaxLimit)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	items, err := h.store.OwnerListRecallTraces(ctx, h.userID, limit)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	receipt := newMemoryOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationTraceList, "trace_list", 0, len(items))
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": items, "receipt": receipt})
}

func (h *memoryOwnerHandler) traceShow(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	if !memoryOwnerProfileAllowed(r, http.MethodGet) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	trace, err := h.store.OwnerFindRecallTrace(ctx, h.userID, id)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	receipt := newMemoryOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationTraceShow, trace.ID, 1, len(trace.Items))
	writeJSON(w, http.StatusOK, map[string]interface{}{"trace": trace, "receipt": receipt})
}

func (h *memoryOwnerHandler) list(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodGet) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query(), "state", "include_inactive", "limit"); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state != "" {
		if err := domainmemory.ValidateMemoryState(state); err != nil {
			writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	includeInactive, err := parseMemoryOwnerBool(r.URL.Query().Get("include_inactive"))
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	items, err := h.store.OwnerListUserMemories(ctx, h.userID, state, includeInactive, limit)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	views := make([]domainmemory.UserMemoryOwnerView, 0, len(items))
	for _, item := range items {
		views = append(views, domainmemory.UserMemoryOwnerViewFromMemory(item))
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	receipt := newMemoryOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationList, "conversation_l1/user_memory/list", 0, len(views))
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": views, "receipt": receipt})
}

func (h *memoryOwnerHandler) propose(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var request struct {
		Type      string `json:"type"`
		Statement string `json:"statement"`
		Reason    string `json:"reason"`
	}
	if err := decodeStrictMemoryOwnerJSON(w, r, &request, 16<<10); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request.Type = strings.TrimSpace(request.Type)
	request.Statement = strings.TrimSpace(request.Statement)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Statement == "" || request.Reason == "" {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := domainmemory.ValidateUserMemoryType(request.Type); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerProposeUserMemory(ctx, requestID, h.userID, h.userID, request.Type, request.Statement, request.Reason)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *memoryOwnerHandler) item(ctx context.Context, w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == http.MethodGet {
		if !memoryOwnerProfileAllowed(r, http.MethodGet) {
			writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
			return
		}
		if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
			writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		item, err := h.store.OwnerFindUserMemory(ctx, h.userID, parts[0])
		if err != nil {
			writeMemoryOwnerStoreError(w, err)
			return
		}
		requestID, ok := memoryOwnerRequestID(ctx)
		if !ok {
			writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
			return
		}
		receipt := newMemoryOwnerReadReceipt(requestID, domainmemory.UserMemoryOwnerOperationShow, item.ID, 1, 1)
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": domainmemory.UserMemoryOwnerViewFromMemory(item), "receipt": receipt})
		return
	}
	if len(parts) != 2 || r.Method != http.MethodPost {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if !memoryOwnerProfileAllowed(r, http.MethodPost) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	operation := strings.TrimSpace(parts[1])
	if operation != domainmemory.UserMemoryOwnerOperationConfirm && operation != domainmemory.UserMemoryOwnerOperationPin && operation != domainmemory.UserMemoryOwnerOperationForget && operation != domainmemory.UserMemoryOwnerOperationSupersede {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	var request struct {
		ReplacementID string `json:"replacement_id"`
		Reason        string `json:"reason"`
	}
	if err := decodeStrictMemoryOwnerJSON(w, r, &request, 8<<10); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request.ReplacementID = strings.TrimSpace(request.ReplacementID)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || (operation == domainmemory.UserMemoryOwnerOperationSupersede && request.ReplacementID == "") || (operation != domainmemory.UserMemoryOwnerOperationSupersede && request.ReplacementID != "") {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerTransitionUserMemory(ctx, requestID, h.userID, h.userID, parts[0], operation, request.ReplacementID, request.Reason)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func memoryOwnerPathSegments(r *http.Request) ([]string, bool) {
	escaped := strings.TrimSuffix(r.URL.EscapedPath(), "/")
	prefix := memoryOwnerRoute + "/"
	if !strings.HasPrefix(escaped, prefix) {
		return nil, false
	}
	rawSuffix := strings.TrimPrefix(escaped, prefix)
	if rawSuffix == "" {
		return nil, false
	}
	rawParts := strings.Split(rawSuffix, "/")
	parts := make([]string, 0, len(rawParts))
	for _, rawPart := range rawParts {
		if rawPart == "" {
			return nil, false
		}
		part, err := url.PathUnescape(rawPart)
		if err != nil || strings.TrimSpace(part) == "" {
			return nil, false
		}
		parts = append(parts, part)
	}
	return parts, true
}

func (h *memoryOwnerHandler) bearerAuthorized(r *http.Request) bool {
	return memoryOwnerBearerAuthorized(r, h.token)
}

func memoryOwnerBearerAuthorized(r *http.Request, token []byte) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, presented, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || presented == "" || strings.IndexAny(presented, " \t\r\n") >= 0 {
		return false
	}
	if !constantTimeMemoryOwnerTokenEqual(token, []byte(presented)) {
		return false
	}
	return true
}

func memoryOwnerClientProfileAllowed(r *http.Request) bool {
	client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
	profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
	if !clientOK || !profileOK || client != "RenCrow_CMD" {
		return false
	}
	if r.Method == http.MethodGet {
		return profile == "cmd-diagnostics"
	}
	if r.Method == http.MethodPost {
		return profile == "cmd-control"
	}
	return true
}

func (h *memoryOwnerHandler) ownerContext(r *http.Request) (context.Context, error) {
	return memoryOwnerOwnerContext(r.Context(), h.userID)
}

func memoryOwnerOwnerContext(ctx context.Context, userID string) (context.Context, error) {
	requestID := uuid.NewString()
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, userID, userID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		return nil, err
	}
	return domaintool.WithToolExecutionScope(ctx, scope), nil
}

func memoryOwnerRequestID(ctx context.Context) (string, bool) {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || strings.TrimSpace(scope.RequestID) == "" {
		return "", false
	}
	return scope.RequestID, true
}

func newMemoryOwnerReadReceipt(requestID, operation, auditReference string, inputCount, outputCount int) domainmemory.UserMemoryOwnerReceipt {
	return domainmemory.UserMemoryOwnerReceipt{
		RequestID:        requestID,
		Operation:        operation,
		Status:           "completed",
		OwnerRoute:       "conversation_l1/user_memory/" + operation,
		PolicyRevision:   domainmemory.UserMemoryOwnerPolicyRevision,
		IdempotencyKey:   requestID,
		IdempotentReplay: false,
		InputCount:       inputCount,
		OutputCount:      outputCount,
		Warnings:         []string{},
		AuditReference:   auditReference,
		CompletedAt:      time.Now().UTC(),
	}
}

func memoryOwnerProfileAllowed(r *http.Request, method string) bool {
	if r.Method != method {
		return false
	}
	profile, ok := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
	if !ok {
		return false
	}
	if method == http.MethodGet {
		return profile == "cmd-diagnostics"
	}
	return profile == "cmd-control"
}

func exactlyOneMemoryOwnerHeader(headers http.Header, name string) (string, bool) {
	values := headers.Values(name)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func constantTimeMemoryOwnerTokenEqual(expected, presented []byte) bool {
	expectedHash := sha256.Sum256(expected)
	presentedHash := sha256.Sum256(presented)
	return subtle.ConstantTimeCompare(expectedHash[:], presentedHash[:]) == 1
}

func memoryOwnerDirectLocalRequest(r *http.Request) bool {
	return security.IsDirectLoopbackRequest(r)
}

func parseMemoryOwnerBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

func parseStrictMemoryOwnerLimit(raw string, defaultValue, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultValue, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 || n > max {
		return 0, errors.New("limit out of range")
	}
	return n, nil
}

func validateMemoryOwnerQuery(values url.Values, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) != 1 {
			return errors.New("unknown or duplicate query parameter")
		}
	}
	return nil
}

func decodeStrictMemoryOwnerJSON(w http.ResponseWriter, r *http.Request, target interface{}, maxBytes int64) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeMemoryOwnerStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerInvalid):
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerNotFound):
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerForbidden):
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerConflict):
		writeMemoryOwnerError(w, http.StatusConflict, "conflict")
	case errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable):
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "store_unavailable")
	default:
		writeMemoryOwnerError(w, http.StatusInternalServerError, "storage_failed")
	}
}

func writeMemoryOwnerError(w http.ResponseWriter, status int, code string) {
	outcome := "rejected"
	if status >= http.StatusInternalServerError {
		outcome = "blocked"
	}
	writeJSON(w, status, map[string]interface{}{
		"status": outcome,
		"error":  map[string]string{"code": code},
	})
}
