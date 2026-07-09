package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

type UserMemoryStore interface {
	CreateUserMemory(ctx context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error)
	ListUserMemories(ctx context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error)
	UpdateUserMemoryState(ctx context.Context, id string, state string, reason string) (*domainmemory.UserMemory, error)
	ForgetUserMemory(ctx context.Context, id string, reason string) (*domainmemory.UserMemory, error)
	SupersedeUserMemory(ctx context.Context, oldID string, newID string, reason string) (*domainmemory.UserMemory, error)
}

func HandleUserMemory(store UserMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "user memory unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleUserMemoryList(w, r, store)
		case http.MethodPost:
			handleUserMemoryCreate(w, r, store)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func HandleUserMemoryState(store UserMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "user memory unavailable") {
			return
		}
		var req struct {
			ID     string `json:"id"`
			State  string `json:"state"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		item, err := store.UpdateUserMemoryState(r.Context(), strings.TrimSpace(req.ID), strings.TrimSpace(req.State), strings.TrimSpace(req.Reason))
		if err != nil {
			http.Error(w, "failed to update user memory state: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": item})
	}
}

func HandleUserMemoryForget(store UserMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "user memory unavailable") {
			return
		}
		var req struct {
			ID     string `json:"id"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		item, err := store.ForgetUserMemory(r.Context(), strings.TrimSpace(req.ID), strings.TrimSpace(req.Reason))
		if err != nil {
			http.Error(w, "failed to forget user memory: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": item})
	}
}

func HandleUserMemorySupersede(store UserMemoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireViewerMethod(w, r, http.MethodPost) {
			return
		}
		if !requireViewerStore(w, store == nil, "user memory unavailable") {
			return
		}
		var req struct {
			ID           string `json:"id"`
			SupersededBy string `json:"superseded_by"`
			Reason       string `json:"reason"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		item, err := store.SupersedeUserMemory(r.Context(), strings.TrimSpace(req.ID), strings.TrimSpace(req.SupersededBy), strings.TrimSpace(req.Reason))
		if err != nil {
			http.Error(w, "failed to supersede user memory: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"item": item})
	}
}

func handleUserMemoryList(w http.ResponseWriter, r *http.Request, store UserMemoryStore) {
	limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
	if err != nil {
		http.Error(w, "invalid limit", http.StatusBadRequest)
		return
	}
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if userID == "" {
		userID = "ren"
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	includeInactive := r.URL.Query().Get("include_inactive") == "1" || strings.EqualFold(r.URL.Query().Get("include_inactive"), "true")
	items, err := store.ListUserMemories(r.Context(), userID, state, includeInactive, limit)
	if err != nil {
		http.Error(w, "failed to list user memories: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"user_id": userID, "items": items})
}

func handleUserMemoryCreate(w http.ResponseWriter, r *http.Request, store UserMemoryStore) {
	var req struct {
		UserID           string   `json:"user_id"`
		Type             string   `json:"type"`
		Statement        string   `json:"statement"`
		State            string   `json:"state"`
		EvidenceEventIDs []string `json:"evidence_event_ids"`
		Confidence       float64  `json:"confidence"`
		Sensitivity      string   `json:"sensitivity"`
		Scope            string   `json:"scope"`
		Source           string   `json:"source"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	item, err := store.CreateUserMemory(r.Context(), domainmemory.CreateUserMemoryInput{
		UserID:           req.UserID,
		Type:             req.Type,
		Statement:        req.Statement,
		State:            req.State,
		EvidenceEventIDs: req.EvidenceEventIDs,
		Confidence:       req.Confidence,
		Sensitivity:      req.Sensitivity,
		Scope:            req.Scope,
		Source:           req.Source,
	})
	if err != nil {
		http.Error(w, "failed to create user memory: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"item": item})
}
