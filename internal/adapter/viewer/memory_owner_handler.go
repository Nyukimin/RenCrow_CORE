package viewer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/google/uuid"
)

const memoryOwnerRoute = "/v1/memory/user"

type MemoryOwnerStore interface {
	OwnerListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error)
	OwnerFindUserMemory(context.Context, string, string) (domainmemory.UserMemory, error)
	OwnerProposeUserMemory(context.Context, string, string, string, string, string, string) (domainmemory.UserMemoryOwnerResult, error)
	OwnerTransitionUserMemory(context.Context, string, string, string, string, string, string, string) (domainmemory.UserMemoryOwnerResult, error)
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
	if !memoryOwnerLoopback(r) {
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

	path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
	switch {
	case path == memoryOwnerRoute && r.Method == http.MethodGet:
		h.list(ctx, w, r)
	case path == memoryOwnerRoute+"/propose" && r.Method == http.MethodPost:
		h.propose(ctx, w, r)
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
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, presented, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || presented == "" || strings.IndexAny(presented, " \t\r\n") >= 0 {
		return false
	}
	if !constantTimeMemoryOwnerTokenEqual(h.token, []byte(presented)) {
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
	requestID := uuid.NewString()
	scope, err := domaintool.NewToolExecutionScope(requestID, domaintool.ActorKindUser, h.userID, h.userID, []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		return nil, err
	}
	return domaintool.WithToolExecutionScope(r.Context(), scope), nil
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

func memoryOwnerLoopback(r *http.Request) bool {
	if r == nil || strings.TrimSpace(r.RemoteAddr) == "" {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
