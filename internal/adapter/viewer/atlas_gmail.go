package viewer

import (
	"context"
	"net/http"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

type AtlasGmailReader interface {
	Receipts(context.Context, int) ([]gmailapp.GmailReceipt, error)
}

type AtlasGmailDiaryBackfillStarter interface{ StartGmailDiaryBackfill() bool }

func (h *atlasHandler) startGmailDiaryBackfill(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !memoryOwnerDirectLocalRequest(r) {
		writeAtlasError(w, http.StatusNotFound, "not_found")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeAtlasError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
	profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
	if !clientOK || !profileOK || client != "RenCrow_CMD" || profile != "cmd-diagnostics" {
		writeAtlasError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAtlasError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	starter, ok := h.gmail.(AtlasGmailDiaryBackfillStarter)
	if !ok {
		writeAtlasError(w, http.StatusServiceUnavailable, "gmail_diary_backfill_unavailable")
		return
	}
	if !starter.StartGmailDiaryBackfill() {
		writeAtlasError(w, http.StatusConflict, "gmail_collection_busy_or_unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *atlasHandler) readGmail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !memoryOwnerDirectLocalRequest(r) {
		writeAtlasError(w, http.StatusNotFound, "not_found")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeAtlasError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !memoryOwnerClientProfileAllowed(r) {
		writeAtlasError(w, http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeAtlasError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if h.gmail == nil {
		writeAtlasError(w, http.StatusServiceUnavailable, "gmail_intake_disabled")
		return
	}
	ctx, err := memoryOwnerOwnerContext(r.Context(), h.userID)
	if err != nil {
		writeAtlasError(w, http.StatusServiceUnavailable, "scope_unavailable")
		return
	}
	receipts, err := h.gmail.Receipts(ctx, 20)
	if err != nil {
		writeAtlasError(w, http.StatusServiceUnavailable, "gmail_receipts_unavailable")
		return
	}
	for i := range receipts {
		receipts[i].Prepared = nil
		receipts[i].PreparedKnowledge = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipts": receipts})
}
