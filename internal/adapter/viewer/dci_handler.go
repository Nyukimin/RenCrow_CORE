package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type DCITraceLister interface {
	ListRecent(limit int) ([]domaindci.SearchTrace, error)
}

type DCITraceContextLister interface {
	ListRecent(ctx context.Context, limit int) ([]domaindci.SearchTrace, error)
}

type DCISearcher interface {
	SearchWithIdentity(context.Context, string, modulecore.TraceID, modulecore.ActionID, string, string, string) (domaindci.SearchResult, error)
}

func HandleDCIRecent(store any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			if n > 100 {
				n = 100
			}
			limit = n
		}
		items := []domaindci.SearchTrace{}
		var err error
		switch s := store.(type) {
		case DCITraceContextLister:
			items, err = s.ListRecent(r.Context(), limit)
		case DCITraceLister:
			items, err = s.ListRecent(limit)
		case nil:
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
			return
		default:
			http.Error(w, "dci trace store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, "failed to load dci traces", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

// NewDCISearchHandler serves the one authenticated, direct-local-only DCI write
// surface. Recent traces remain a separate public read-only projection.
func NewDCISearchHandler(searcher DCISearcher, userID string, token []byte) http.HandlerFunc {
	return (&dciSearchHandler{
		searcher: searcher,
		userID:   strings.TrimSpace(userID),
		token:    append([]byte(nil), token...),
	}).ServeHTTP
}

type dciSearchHandler struct {
	searcher DCISearcher
	userID   string
	token    []byte
}

func (h *dciSearchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !memoryOwnerDirectLocalRequest(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.userID == "" || len(h.token) == 0 {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "scope_unavailable")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !memoryOwnerClientProfileAllowed(r) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if h.searcher == nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "dci_unavailable")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeMemoryOwnerError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	contentType, _, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || contentType != "application/json" {
		writeMemoryOwnerError(w, http.StatusUnsupportedMediaType, "content_type_required")
		return
	}
	query, err := decodeDCISearchQuery(w, r)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, err := memoryOwnerOwnerContext(r.Context(), h.userID)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "scope_unavailable")
		return
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != h.userID || scope.AuthenticatedUserID != h.userID || !scope.Allows(domaintool.DataScopeUser) || strings.TrimSpace(scope.RequestID) == "" {
		writeMemoryOwnerError(w, http.StatusServiceUnavailable, "scope_unavailable")
		return
	}
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	result, err := h.searcher.SearchWithIdentity(ctx, query, traceID, actionID, string(scope.ActorKind), scope.ActorID, scope.RequestID)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "dci_search_failed")
		return
	}
	if err := validateDCIViewerSearchResult(result, query, traceID, actionID, scope); err != nil {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "dci_search_failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeDCISearchQuery(w http.ResponseWriter, r *http.Request) (string, error) {
	if r.Body == nil {
		return "", errors.New("dci search request body is required")
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return "", errors.New("dci search request must be an object")
	}
	seen := false
	var query string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", err
		}
		key, ok := keyToken.(string)
		if !ok || key != "query" {
			return "", errors.New("dci search request has an unknown field")
		}
		if seen {
			return "", errors.New("dci search request has a duplicate query")
		}
		seen = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return "", err
		}
		if err := json.Unmarshal(raw, &query); err != nil {
			return "", fmt.Errorf("dci search query must be a string: %w", err)
		}
	}
	closingToken, err := decoder.Token()
	if err != nil {
		return "", err
	}
	closingDelim, ok := closingToken.(json.Delim)
	if !ok || closingDelim != '}' {
		return "", errors.New("dci search request object is not closed")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("dci search request has trailing content")
		}
		return "", err
	}
	query = strings.TrimSpace(query)
	if !seen || query == "" {
		return "", errors.New("dci search query is required")
	}
	return query, nil
}

func validateDCIViewerSearchResult(result domaindci.SearchResult, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, scope domaintool.ToolExecutionScope) error {
	if err := domaindci.ValidateSearchResult(result); err != nil {
		return err
	}
	if result.Trace.TraceID != traceID || result.Trace.ActionID != actionID || result.Pack.ActionID != actionID {
		return errors.New("dci search result canonical id mismatch")
	}
	if result.Trace.ActorAttribution != domaindci.ActorAttributionAuthenticated || result.Trace.ActorKind != string(scope.ActorKind) || result.Trace.ActorID != scope.ActorID {
		return errors.New("dci search result actor mismatch")
	}
	if result.Trace.IdempotencyKey != scope.RequestID || result.Trace.Mode != "dci" || result.Trace.UserQuery != query || result.Pack.Query != query {
		return errors.New("dci search result request mismatch")
	}
	return nil
}
