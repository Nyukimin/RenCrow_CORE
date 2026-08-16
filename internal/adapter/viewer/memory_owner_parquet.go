package viewer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const memoryOwnerParquetExportRoute = "/viewer/memory/export"

func (h *memoryOwnerHandler) parquetExport(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
	result, err := h.store.OwnerExportConversationArchiveParquet(ctx, requestID, h.userID, h.userID)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *memoryOwnerHandler) parquetVerify(ctx context.Context, w http.ResponseWriter, r *http.Request, targetID string) {
	if !memoryOwnerProfileAllowed(r, http.MethodGet) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := validateMemoryOwnerQuery(r.URL.Query()); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := rejectMemoryOwnerBody(w, r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ok := memoryOwnerRequestID(ctx)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "scope_unavailable")
		return
	}
	result, err := h.store.OwnerVerifyConversationArchiveParquet(ctx, requestID, h.userID, h.userID, targetID)
	if err != nil {
		writeMemoryOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func memoryOwnerParquetTargetID(escapedPath string) (string, bool) {
	prefix := memoryOwnerParquetExportRoute + "/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", false
	}
	rawID := strings.TrimPrefix(escapedPath, prefix)
	if rawID == "" || strings.Contains(rawID, "/") {
		return "", false
	}
	targetID, err := url.PathUnescape(rawID)
	if err != nil || strings.TrimSpace(targetID) == "" || strings.ContainsAny(targetID, "/\\") {
		return "", false
	}
	return targetID, true
}

func rejectMemoryOwnerBody(w http.ResponseWriter, r *http.Request) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		return err
	}
	if len(body) != 0 {
		return errors.New("request body is not allowed")
	}
	return nil
}
